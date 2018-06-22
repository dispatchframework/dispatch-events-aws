///////////////////////////////////////////////////////////////////////
// Copyright (c) 2017 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0
///////////////////////////////////////////////////////////////////////

package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchevents"
	"github.com/aws/aws-sdk-go/service/sqs"
	uuid "github.com/satori/go.uuid"

	"github.com/vmware/dispatch/pkg/events"
	"github.com/vmware/dispatch/pkg/events/driverclient"
)

var sess *session.Session
var sqsService *sqs.SQS
var cweService *cloudwatchevents.CloudWatchEvents
var driverClient driverclient.Client
var sqsQueueURL, sqsQueueARN *string

// AWS args
var awsRegion = flag.String("region", "us-west-2", "Set aws region")
var awsAKId = os.Getenv("AWS_ACCESS_KEY_ID")
var awsSecretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")

// CloudWatch args
type cwRuleEntry struct {
	name string
	arn  string
}

var cwRules []cwRuleEntry
var eventPatterns = flag.String("event-patterns", "", "Event patterns for AWS CloudWatch Rule(json string slice)")
var scheduleExpression = flag.String("schedule-expression", "", "Schedule expression, For example, cron(0 20 * * ? *) or rate(5 minutes).")

// SQS args
var sqsQueueName = flag.String("queue-name", "dispatch", "Set SQS queue name, will be used as CloudWatch Rule target")
var fetchDuration = flag.Int64("duration", 20, "Fetching duration in seconds")

// Dispatch Event args
var eventSource = flag.String("source", "dispatch", "Set custom event source for the driver")

// clean up resources after shutting down
var cleanUp = flag.Bool("clean-up", false, "Clean up AWS resources after shutting down")

// debug
var dryRun = flag.Bool("dry-run", false, "Debug, pull messages and do not send Dispatch events")

func getSession() *session.Session {
	return session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region:      awsRegion,
			Credentials: credentials.NewStaticCredentials(awsAKId, awsSecretKey, ""),
		},
	}))
}

func getQueueURLAndARN(queueName *string) (url, arn *string) {

	urlRes, err := sqsService.GetQueueUrl(&sqs.GetQueueUrlInput{
		QueueName: queueName,
	})

	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case sqs.ErrCodeQueueDoesNotExist:
				log.Printf("Given SQS queue doesn't exist, creating one ..\n")
				longPollingDuration := strconv.Itoa(20)

				// Create queue with long-polling enabled
				createOutputs, err := sqsService.CreateQueue(&sqs.CreateQueueInput{
					QueueName: queueName,
					Attributes: map[string]*string{
						"ReceiveMessageWaitTimeSeconds": &longPollingDuration,
					},
				})
				if err != nil {
					panic(err)
				}
				log.Printf("Queue - %s created.\n", *queueName)
				url = createOutputs.QueueUrl
			}
		} else {
			panic(err)
		}
	} else {
		url = urlRes.QueueUrl
	}

	// get queue arn
	queueArnAttribute := aws.String("QueueArn")
	getQueueAttributeOutput, _ := sqsService.GetQueueAttributes(&sqs.GetQueueAttributesInput{
		QueueUrl: url,
		AttributeNames: []*string{
			queueArnAttribute,
		},
	})
	arn = getQueueAttributeOutput.Attributes[*queueArnAttribute]

	return
}

func handleEvent(m *sqs.Message) {
	if *dryRun {
		return
	}

	byt := []byte(*m.Body)
	var dat map[string]interface{}
	if err := json.Unmarshal(byt, &dat); err != nil {
		panic(err)
	}

	ev := &events.CloudEvent{
		EventType:          dat["source"].(string),
		CloudEventsVersion: events.CloudEventsVersion,
		Source:             *eventSource,
		EventID:            uuid.NewV4().String(),
		EventTime:          time.Now(),
		ContentType:        "application/json",
		Data:               json.RawMessage(*m.Body),
	}
	// Push event to Dispatch
	if err := driverClient.SendOne(ev); err != nil {
		log.Printf("Error sending event %s", err)
	} else {
		bytes, _ := json.MarshalIndent(ev, "", "    ")
		log.Printf("Event sent to Dispatch: %s\n", string(bytes))
	}
}

func receiveAndDeleteMessage(qURL *string) (e error) {

	// Receive message
	maxNumberOfMessages := int64(10)
	receiveOutput, err := sqsService.ReceiveMessage(&sqs.ReceiveMessageInput{
		QueueUrl:            qURL,
		MaxNumberOfMessages: &maxNumberOfMessages,
	})
	if err != nil {
		log.Printf("Receive message failed: %s\n", err.Error())
		e = err
	} else {
		for _, m := range receiveOutput.Messages {
			log.Printf("Received Event: %s\n", m.String())
			handleEvent(m)
			// Delete message
			_, err := sqsService.DeleteMessage(&sqs.DeleteMessageInput{
				QueueUrl:      qURL,
				ReceiptHandle: m.ReceiptHandle,
			})
			if err != nil {
				log.Printf("Delete message failed: %s\n", err.Error())
				e = err
			}
		}
	}
	return
}

func getDriverClient() driverclient.Client {
	if *dryRun {
		return nil
	}

	client, err := driverclient.NewHTTPClient()
	if err != nil {
		panic(err)
	}
	log.Println("Event driver initialized.")
	return client
}

func addQueuePermissionForRules(rules []cwRuleEntry, qURL, qARN *string) error {

	// Add required permission to sqs target
	// https://docs.aws.amazon.com/AmazonCloudWatch/latest/events/resource-based-policies-cwe.html
	// https://docs.aws.amazon.com/sdk-for-go/v1/developer-guide/iam-example-policies.html
	type (
		ConditionEntry struct {
			ArnEquals map[string]string
		}

		StatementEntry struct {
			Sid       string
			Effect    string
			Action    string
			Resource  string
			Condition ConditionEntry
			Principal map[string]string
		}

		PolicyEntry struct {
			Version   string
			Id        string
			Statement []StatementEntry
		}
	)

	statements := []StatementEntry{}
	for _, rule := range rules {
		statements = append(statements, StatementEntry{
			Sid:      "Trust-" + rule.name + "-toSendMessage",
			Effect:   "Allow",
			Action:   "sqs:SendMessage",
			Resource: *qARN,
			Condition: ConditionEntry{
				ArnEquals: map[string]string{
					"aws:SourceArn": rule.arn,
				},
			},
			Principal: map[string]string{
				"Service": "events.amazonaws.com",
			},
		})
	}

	policy := PolicyEntry{
		Version:   "2012-10-17",
		Id:        *qARN + "/SQSDefaultPolicy",
		Statement: statements,
	}
	policyBytes, _ := json.Marshal(policy)
	policyString := string(policyBytes)
	log.Printf("Policy: %s\n", policyString)

	_, err := sqsService.SetQueueAttributes(&sqs.SetQueueAttributesInput{
		QueueUrl: qURL,
		Attributes: map[string]*string{
			"Policy": aws.String(policyString),
		},
	})
	if err != nil {
		return err
	}

	log.Printf("Attributes set and put permission done.")
	return nil
}

func putRuleAndTargetForScheduleExp(rN, scheduleExp, qName, qURL, qARN *string) {

	log.Println("putting rule for: ", *scheduleExp)
	// put rule
	var (
		putRuleOutput *cloudwatchevents.PutRuleOutput
		err           error
	)
	putRuleOutput, err = cweService.PutRule(&cloudwatchevents.PutRuleInput{
		Name:               rN,
		Description:        aws.String("Dispatch AWS event driver rule with Schedule Experssion"),
		ScheduleExpression: scheduleExp,
		State:              aws.String(cloudwatchevents.RuleStateEnabled),
	})

	if err != nil {
		panic(err)
	}
	log.Printf("PutRuleOutput: %s\n", putRuleOutput.String())

	cwRules = append(cwRules, cwRuleEntry{
		name: *rN,
		arn:  *putRuleOutput.RuleArn,
	})

	// put queue as target in rule
	_, err = cweService.PutTargets(&cloudwatchevents.PutTargetsInput{
		Rule: rN,
		Targets: []*cloudwatchevents.Target{
			&cloudwatchevents.Target{
				Id:  qName,
				Arn: qARN,
			},
		},
	})
	if err != nil {
		panic(err)
	}
	log.Printf("Put schedule expression rule and target done. \n")
}

func putRuleAndTargetForEventPattern(rN, pattern, qName, qURL, qARN *string) {

	log.Println("putting rule for: ", *pattern)
	// put rule
	var (
		putRuleOutput *cloudwatchevents.PutRuleOutput
		err           error
	)
	putRuleOutput, err = cweService.PutRule(&cloudwatchevents.PutRuleInput{
		Name:         rN,
		Description:  aws.String("Dispatch AWS event driver rule with EventPattern"),
		EventPattern: pattern,
		State:        aws.String(cloudwatchevents.RuleStateEnabled),
	})

	if err != nil {
		panic(err)
	}
	log.Printf("PutRuleOutput: %s\n", putRuleOutput.String())

	cwRules = append(cwRules, cwRuleEntry{
		name: *rN,
		arn:  *putRuleOutput.RuleArn,
	})

	// put queue as target in rule
	_, err = cweService.PutTargets(&cloudwatchevents.PutTargetsInput{
		Rule: rN,
		Targets: []*cloudwatchevents.Target{
			&cloudwatchevents.Target{
				Id:  qName,
				Arn: qARN,
			},
		},
	})
	if err != nil {
		panic(err)
	}
	log.Printf("Put event pattern rule and target done. \n")
}

func generateRuleName() *string {
	rN := "dispatch-rule-" + uuid.NewV4().String()
	return &rN
}

func prepare() {

	flag.Parse()
	// make queue name unique to avoid '60 seconds' waiting time
	fullName := *sqsQueueName + "-" + uuid.NewV4().String()
	sqsQueueName = &fullName

	// init aws session and service
	sess = getSession()
	sqsService = sqs.New(sess)
	cweService = cloudwatchevents.New(sess)

	// get SQS url or create new one
	sqsQueueURL, sqsQueueARN = getQueueURLAndARN(sqsQueueName)

	// put rules for schedule expressions
	if *scheduleExpression != "" {
		putRuleAndTargetForScheduleExp(generateRuleName(), scheduleExpression, sqsQueueName, sqsQueueURL, sqsQueueARN)
	}

	// put rules for addtional eventPatterns
	if *eventPatterns != "" {
		var patterns []map[string]interface{}
		err := json.Unmarshal([]byte(*eventPatterns), &patterns)
		if err != nil {
			log.Printf("Error parsing event patterns: %s \n", *eventPatterns)
		} else {
			for _, p := range patterns {
				pBytes, _ := json.Marshal(p)
				pStr := string(pBytes)
				putRuleAndTargetForEventPattern(generateRuleName(), &pStr, sqsQueueName, sqsQueueURL, sqsQueueARN)
			}
		}
	}

	// add SQS persmission for all rules
	addQueuePermissionForRules(cwRules, sqsQueueURL, sqsQueueARN)

	// init Dispatch driver client
	driverClient = getDriverClient()
}

func cleanUpResources() {
	log.Println("Cleaning up AWS resources...")
	_, err := sqsService.DeleteQueue(&sqs.DeleteQueueInput{
		QueueUrl: sqsQueueURL,
	})
	if err == nil {
		log.Println("SQS Queue deleted.")
	}

	for _, rule := range cwRules {
		ruleName := rule.name
		_, err = cweService.RemoveTargets(&cloudwatchevents.RemoveTargetsInput{
			Rule: &ruleName,
			Ids: []*string{
				sqsQueueName,
			},
		})

		_, err = cweService.DeleteRule(&cloudwatchevents.DeleteRuleInput{
			Name: &ruleName,
		})

		if err == nil {
			log.Printf("CloudWatch Rule %s deleted.\n", ruleName)
		} else {
			log.Println(err.Error())
		}
	}
}

func main() {
	defer func() {
		if *cleanUp {
			cleanUpResources()
		}
	}()

	prepare()

	// Create ticker and chan signal
	loopDone := make(chan os.Signal)
	ticker := time.NewTicker(time.Duration(int64(*fetchDuration)) * time.Second)
	signal.Notify(loopDone, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			log.Println("Fetching new messages...")
			go receiveAndDeleteMessage(sqsQueueURL)
		case <-loopDone:
			log.Printf("Shutting down event driver...")
			return
		}
	}

}
