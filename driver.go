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
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"
	uuid "github.com/satori/go.uuid"

	"github.com/vmware/dispatch/pkg/events"
	"github.com/vmware/dispatch/pkg/events/driverclient"
)

var sess *session.Session
var svc *sqs.SQS
var driverClient driverclient.Client
var queueURL string

var awsRegion = flag.String("region", "us-west-2", "Set aws region")
var awsAKId = flag.String("access-key-id", "", "AWS credential access id [aws_access_key_id] field")
var awsSecretKey = flag.String("secret-key", "", "AWS credential secret access key [aws_secret_access_key] field")
var sqsQueueName = flag.String("queue-name", "dispatch", "Set SQS queue name")
var fetchDuration = flag.Int64("duration", 20, "Fetching duration in seconds")

var eventNamespace = flag.String("namespace", "dispatchframework.io/aws-event", "Set event namespace")
var eventSourceID = flag.String("source-id", uuid.NewV4().String(), "Set custom Source ID for the driver")

func getSession() *session.Session {
	return session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region:      aws.String(*awsRegion),
			Credentials: credentials.NewStaticCredentials(*awsAKId, *awsSecretKey, ""),
		},
	}))
}

func getQueueURL(queueName *string) string {
	urlRes, err := svc.GetQueueUrl(&sqs.GetQueueUrlInput{
		QueueName: aws.String(*queueName),
	})
	if err != nil {
		panic(err)
	}
	return *urlRes.QueueUrl
}

func handleEvent(m *sqs.Message) {
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

func receiveAndDeleteMessage(qN string) error {

	var result error
	// Receive message
	maxNumberOfMessages := int64(10)
	receiveOutput, err := svc.ReceiveMessage(&sqs.ReceiveMessageInput{
		QueueUrl:            &queueURL,
		MaxNumberOfMessages: &maxNumberOfMessages,
	})
	if err != nil {
		log.Printf("Receive message failed: %s\n", err.Error())
		result = err
	} else {
		for _, m := range receiveOutput.Messages {
			handleEvent(m)
			// Delete message
			_, err := svc.DeleteMessage(&sqs.DeleteMessageInput{
				QueueUrl:      &queueURL,
				ReceiptHandle: m.ReceiptHandle,
			})
			if err != nil {
				log.Printf("Delete message failed: %s\n", err.Error())
				result = err
			}
		}
	}
	return result
}

func prepare() {

	flag.Parse()

	// init aws session and service
	sess = getSession()
	svc = sqs.New(sess)
	queueURL = getQueueURL(sqsQueueName)

	// init Dispatch driver client
	var err error
	driverClient, err = driverclient.NewHTTPClient()
	if err != nil {
		panic(err)
	}
	log.Println("Event driver initialized.")
}

func main() {

	prepare()

	// Create ticker and chan signal
	loopDone := make(chan os.Signal)
	ticker := time.NewTicker(time.Duration(int64(*fetchDuration)) * time.Second)
	signal.Notify(loopDone, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			log.Println("Fetching new messages...")
			go receiveAndDeleteMessage(*sqsQueueName)
		case <-loopDone:
			log.Printf("Shutting down event driver...")
			return
		}
	}

}
