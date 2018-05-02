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
var sqsQueueURL *string

// AWS args
var awsRegion = flag.String("region", "us-west-2", "Set aws region")
var awsAKId = flag.String("access-key-id", "", "AWS credential access id [aws_access_key_id] field")
var awsSecretKey = flag.String("secret-key", "", "AWS credential secret access key [aws_secret_access_key] field")

// CloudWatch args
var ruleName = flag.String("rule-name", "dispatch", "Rule name in CloudWatch event")
var eventPattern = flag.String("event-pattern", "", "Event pattern for AWS CloudWatch Rule, should be a JSON string, for example: {\"source\":[\"aws.autoscaling\"]}")
var scheduleExpression = flag.String("schedule-expression", "", "Schedule expression, For example, cron(0 20 * * ? *) or rate(5 minutes).")

// SQS args
var sqsQueueName = flag.String("queue-name", "dispatch", "Set SQS queue name, will be used as CloudWatch Rule target")
var fetchDuration = flag.Int64("duration", 20, "Fetching duration in seconds")

// Dispatch Event args
var eventNamespace = flag.String("namespace", "dispatchframework.io/aws-event", "Set event namespace")
var eventSourceID = flag.String("source-id", uuid.NewV4().String(), "Set custom Source ID for the driver")

// clean up resources after shutting down
var cleanUp = flag.Bool("clean-up", false, "Clean up AWS resources after shutting down")

// debug
var dryRun = flag.Bool("dry-run", false, "Debug, pull messages and do not send Dispatch events")

func getSession() *session.Session {
	return session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region:      awsRegion,
			Credentials: credentials.NewStaticCredentials(*awsAKId, *awsSecretKey, ""),
		},
	}))
}

func getQueueURL(queueName *string) (url *string) {

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
		Namespace:          *eventNamespace,
		EventType:          dat["source"].(string),
		CloudEventsVersion: events.CloudEventsVersion,
		SourceType:         "aws",
		SourceID:           *eventSourceID,
		EventID:            uuid.NewV4().String(),
		EventTime:          time.Now(),
		Data:               m.String(),
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

func addQueuePermission(rArn, qURL, qArn *string) error {
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
	policy := PolicyEntry{
		Version: "2012-10-17",
		Id:      *qArn + "/SQSDefaultPolicy",
		Statement: []StatementEntry{
			StatementEntry{
				Sid:      "TrustCWEToSendEventsToSQS",
				Effect:   "Allow",
				Action:   "sqs:SendMessage",
				Resource: *qArn,
				Condition: ConditionEntry{
					ArnEquals: map[string]string{
						"aws:SourceArn": *rArn,
					},
				},
				Principal: map[string]string{
					"Service": "events.amazonaws.com",
				},
			},
		},
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

	log.Printf("Attributes set")
	return nil
}

func putRuleAndTarget(rN, pattern, qURL, qName *string) {

	// put rule
	var (
		putRuleOutput *cloudwatchevents.PutRuleOutput
		err           error
	)
	if *scheduleExpression == "" {
		putRuleOutput, err = cweService.PutRule(&cloudwatchevents.PutRuleInput{
			Name:         rN,
			Description:  aws.String("Dispatch AWS event driver rule with EventPattern"),
			EventPattern: pattern,
			State:        aws.String(cloudwatchevents.RuleStateEnabled),
		})
	} else {
		putRuleOutput, err = cweService.PutRule(&cloudwatchevents.PutRuleInput{
			Name:               rN,
			Description:        aws.String("Dispatch AWS event driver rule with Schedule Experssion"),
			ScheduleExpression: scheduleExpression,
			State:              aws.String(cloudwatchevents.RuleStateEnabled),
		})
	}
	if err != nil {
		panic(err)
	}
	log.Printf("PutRuleOutput: %s\n", putRuleOutput.String())

	// get queue arn
	queueArnAttribute := string("QueueArn")
	getQueueAttributeOutput, _ := sqsService.GetQueueAttributes(&sqs.GetQueueAttributesInput{
		QueueUrl: qURL,
		AttributeNames: []*string{
			&queueArnAttribute,
		},
	})
	queueArn := getQueueAttributeOutput.Attributes[queueArnAttribute]
	log.Printf("Queue ARN: %s \n", *queueArn)

	// before put target, add permission to sqs target. Allow CloudWatch to SendMessage to sqs.
	if err := addQueuePermission(putRuleOutput.RuleArn, qURL, queueArn); err != nil {
		log.Printf("Failed to add permission to SQS queue: %s. Please make sure user has required privileges. \n", err.Error())
	}

	// put queue as target in rule
	_, err = cweService.PutTargets(&cloudwatchevents.PutTargetsInput{
		Rule: rN,
		Targets: []*cloudwatchevents.Target{
			&cloudwatchevents.Target{
				Id:  qName,
				Arn: queueArn,
			},
		},
	})
	if err != nil {
		panic(err)
	}
	log.Printf("Put rule and target done. \n")
}

func prepare() {

	flag.Parse()

	// init aws session and service
	sess = getSession()
	sqsService = sqs.New(sess)
	cweService = cloudwatchevents.New(sess)

	// get SQS url or create new one
	sqsQueueURL = getQueueURL(sqsQueueName)

	// put rule
	putRuleAndTarget(ruleName, eventPattern, sqsQueueURL, sqsQueueName)

	// init Dispatch driver client
	driverClient = getDriverClient()
}

func cleanUpResources(rName, qURL, qName *string) {
	log.Println("Cleaning up AWS resources...")
	_, err := sqsService.DeleteQueue(&sqs.DeleteQueueInput{
		QueueUrl: qURL,
	})
	if err == nil {
		log.Println("SQS Queue deleted.")
	}

	_, err = cweService.RemoveTargets(&cloudwatchevents.RemoveTargetsInput{
		Rule: rName,
		Ids: []*string{
			qName,
		},
	})
	_, err = cweService.DeleteRule(&cloudwatchevents.DeleteRuleInput{
		Name: rName,
	})
	if err == nil {
		log.Println("CloudWatch Rule deleted.")
	}
}

func main() {
	defer func() {
		if *cleanUp {
			cleanUpResources(ruleName, sqsQueueURL, sqsQueueName)
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
