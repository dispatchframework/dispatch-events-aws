///////////////////////////////////////////////////////////////////////
// Copyright (c) 2017 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0
///////////////////////////////////////////////////////////////////////

package main

import (
	"encoding/json"
	"flag"
	"fmt"
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
	"github.com/aws/aws-sdk-go/service/cloudtrail"
	"github.com/aws/aws-sdk-go/service/cloudwatchevents"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/aws/aws-sdk-go/service/sts"
	uuid "github.com/satori/go.uuid"

	"github.com/vmware/dispatch/pkg/events"
	"github.com/vmware/dispatch/pkg/events/driverclient"
)

var sess *session.Session
var sqsService *sqs.SQS
var cweService *cloudwatchevents.CloudWatchEvents
var ctService *cloudtrail.CloudTrail
var s3Service *s3.S3
var stsService *sts.STS
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
var eventPatterns = flag.String("event-patterns", "", "event patterns for AWS cloudwatch rule (json string slice)")
var scheduleExpression = flag.String("schedule-expression", "", "Schedule expression, For example, cron(0 20 * * ? *) or rate(5 minutes).")

// SQS args
var sqsQueueName = flag.String("queue-name", "dispatch", "Set SQS queue name prefix")
var fetchDuration = flag.Int64("duration", 20, "Fetching duration in seconds")

// CloudTrail and s3 bucket args
var bucketName = flag.String("bucket-name", "dispatch-events-aws-tweet-example", "bucket name")
var trailName = flag.String("trail-name", "dispatch-events-aws-"+uuid.NewV4().String(), "Set CloudTrail name")

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
		Namespace:          *eventNamespace,
		EventType:          dat["source"].(string),
		CloudEventsVersion: events.CloudEventsVersion,
		SourceType:         "aws",
		SourceID:           *eventSourceID,
		EventID:            uuid.NewV4().String(),
		EventTime:          time.Now(),
		ContentType:        "application/json",
		Data:               *m.Body,
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

func addQueuePermission(rules []cwRuleEntry, qURL, qARN *string) error {

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
		log.Println(err)
		return err
	}

	log.Printf("Attributes set and put permission done.")
	return nil
}

func makeS3EventPattern(bucketName *string) string {
	pattern := map[string]interface{}{
		"source": []string{
			"aws.s3",
		},
		"detail-type": []string{
			"AWS API Call via CloudTrail",
		},
		"detail": map[string]interface{}{
			"eventSource": []string{
				"s3.amazonaws.com",
			},
			"eventName": []string{
				"PutObject",
			},
			"requestParameters": map[string]interface{}{
				"bucketName": []string{
					*bucketName,
				},
			},
		},
	}
	r, _ := json.Marshal(pattern)
	return string(r)
}

func putRuleAndTarget(rN, pattern, qName, qURL, qARN *string) {

	// put rule
	var (
		putRuleOutput *cloudwatchevents.PutRuleOutput
		err           error
	)

	log.Printf("Event Pattern: %s", *pattern)
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
	log.Printf("Put rule and target done. \n")
}

// create cloud trail and upload trail to bucket
func createCloudTrail(trailName, bucketName *string) {

	// first check bucket existence
	listBucketsOutput, err := s3Service.ListBuckets(&s3.ListBucketsInput{})
	if err != nil {
		log.Println("Unable to list buckets. continue")
	}
	exist := false
	for _, bucket := range listBucketsOutput.Buckets {
		if *bucket.Name == *bucketName {
			exist = true
			break
		}
	}
	if !exist {
		// create s3 bucket and images folder
		callerIdentityOutput, err := stsService.GetCallerIdentity(&sts.GetCallerIdentityInput{})
		if err != nil {
			panic(err)
		}

		callerID := aws.StringValue(callerIdentityOutput.Account)

		s3Policy := map[string]interface{}{
			"Version": "2012-10-17",
			"Statement": []map[string]interface{}{
				{
					"Sid":    "AWSCloudTrailAclCheck",
					"Effect": "Allow",
					"Principal": map[string]interface{}{
						"Service": "cloudtrail.amazonaws.com",
					},
					"Action":   "s3:GetBucketAcl",
					"Resource": "arn:aws:s3:::" + *bucketName,
				},
				{
					"Sid":    "AWSCloudTrailWrite",
					"Effect": "Allow",
					"Principal": map[string]interface{}{
						"Service": "cloudtrail.amazonaws.com",
					},
					"Action":   "s3:PutObject",
					"Resource": "arn:aws:s3:::" + *bucketName + "/AWSLogs/" + callerID + "/*",
					"Condition": map[string]interface{}{
						"StringEquals": map[string]interface{}{
							"s3:x-amz-acl": "bucket-owner-full-control",
						},
					},
				},
			},
		}

		policy, err := json.Marshal(s3Policy)
		if err != nil {
			fmt.Println("Error marshalling request")
			os.Exit(0)
		}

		_, err = s3Service.CreateBucket(&s3.CreateBucketInput{
			Bucket: bucketName,
			CreateBucketConfiguration: &s3.CreateBucketConfiguration{
				LocationConstraint: aws.String(*awsRegion),
			},
		})
		if err != nil {
			panic(err)
		}

		_, err = s3Service.PutBucketPolicy(&s3.PutBucketPolicyInput{
			Bucket: bucketName,
			Policy: aws.String(string(policy)),
		})
		if err != nil {
			panic(err)
		}

		err = s3Service.WaitUntilBucketExists(&s3.HeadBucketInput{
			Bucket: bucketName,
		})
		log.Println("Create s3 bucket done.")

		// put folder
		_, err = s3Service.PutObject(&s3.PutObjectInput{
			Bucket: bucketName,
			Key:    aws.String("images/"),
		})
		if err != nil {
			panic(err)
		}
		log.Println("S3 object images/ created.")
	}

	_, err = ctService.CreateTrail(&cloudtrail.CreateTrailInput{
		Name:         trailName,
		S3BucketName: bucketName,
	})
	if err != nil {
		panic(err)
	}

	_, err = ctService.StartLogging(&cloudtrail.StartLoggingInput{
		Name: trailName,
	})
	if err != nil {
		panic(err)
	}

	_, err = ctService.PutEventSelectors(&cloudtrail.PutEventSelectorsInput{
		TrailName: trailName,
		EventSelectors: []*cloudtrail.EventSelector{
			{
				DataResources: []*cloudtrail.DataResource{
					{
						Values: []*string{
							aws.String("arn:aws:s3:::" + *bucketName + "/images"),
						},
						Type: aws.String("AWS::S3::Object"),
					},
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	log.Println("Create CloudTrail and event selector done.")
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
	ctService = cloudtrail.New(sess)
	s3Service = s3.New(sess)
	stsService = sts.New(sess)

	// get SQS url & arn or create new one
	sqsQueueURL, sqsQueueARN = getQueueURLAndARN(sqsQueueName)

	// put rule for s3 bucket
	pattern := makeS3EventPattern(bucketName)
	putRuleAndTarget(generateRuleName(), &pattern, sqsQueueName, sqsQueueURL, sqsQueueARN)

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
				putRuleAndTarget(generateRuleName(), &pStr, sqsQueueName, sqsQueueURL, sqsQueueARN)
			}
		}
	}

	// add SQS persmission for all rules
	addQueuePermission(cwRules, sqsQueueURL, sqsQueueARN)

	// init cloudtrail and policy
	createCloudTrail(trailName, bucketName)

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
	} else {
		log.Println(err.Error())
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

	_, err = ctService.DeleteTrail(&cloudtrail.DeleteTrailInput{
		Name: trailName,
	})
	if err == nil {
		log.Println("Cloudtrail delted.")
	} else {
		log.Println(err.Error())
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
