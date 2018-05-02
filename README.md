## Dispatch Event Driver for AWS
This repo contains a template for aws SQS event driver support for Dispatch.

### 1. Get AWS credentials
Follow [`Get AWS Access Key`](https://docs.aws.amazon.com/sdk-for-go/v1/developer-guide/setting-up.html) to obtain aws credential and aws region, take a note for `<aws_region>`, `<aws_access_key_id>` and `<aws_secret_access_key>`. Create env varbiable for these two fields:
```bash
$ export AWS_AKID=<aws_access_key_id>
$ export AWS_SECRET_KEY=<aws_secret_access_key>
```

### 2. Build Event Driver Image
```bash
$ docker build -t dispatch-events-aws .
```

### 3. Create Eventdrivertype in Dispatch
```bash
$ dispatch create eventdrivertype aws-sqs dispatch-events-aws:latest
Created event driver type: aws-sqs
```

### 4. Create Eventdriver in Dispatch
When creating AWS-eventdriver, following arguments should be specified properly:
* `region`, `access-key-id` and `secret-key` : obtained from step 1
* Ways to invoke target: *Event Pattern* or *Schedule*:
  * `event-pattern` : setting up event pattern for AWS CloudWatch Rule, refer to [AWS Event Pattern](https://docs.aws.amazon.com/AmazonCloudWatch/latest/events/CloudWatchEventsandEventPatterns.html) for more details.
  * `schedule-expression` : specifying event schedule expression, please refer to [AWS Schedule Events](https://docs.aws.amazon.com/AmazonCloudWatch/latest/events/ScheduledEvents.html) for details.

Following parameters are optional (have defaults):
* `rule-name` : specifying CloudWatch Rule name, default "*dispatch*"
* `queue-name` : specifying SQS queue name, default "*dispatch*"
* `duration` : specifying SQS polling message duration, default "*20*" (0-20 seconds), wich enables [Long-polling](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-long-polling.html)
* `namespace` : specifying event namespace, default "*dispatchframework.io/aws-event*"
* `source-id` : specifying event source id, default a new UUID string
* `clean-up` : clean up AWS resources after Driver shuts down. default *false*


For example, create a event driver by `event-pattern`:
```bash
$ dispatch create eventdriver aws-sqs --set region="us-west-2" --set access-key-id=${AWS_AKID} --set secret-key=${AWS_SECRET_KEY} --set clean-up="true" --set event-pattern="{\"source\":[\"aws.autoscaling\"]}"
Created event driver: holy-grackle-805996
```

or by `schedule-expression`:
```bash
$ dispatch create eventdriver aws-sqs --set region="us-west-2" --set access-key-id=${AWS_AKID} --set secret-key=${AWS_SECRET_KEY} --set clean-up="true" --set schedule-expression="rate(1 minute)"
Created event driver: trusting-marlin-576200
```


### 5. Create Subscription in Dispatch
For example, create a `aws.autoscaling` event subscription:
```bash
$ dispatch create subscription hello-py --event-type="aws.autoscaling" --source-type="aws"
created subscription: innocent-werewolf-420270
```

`source-type` should be `aws`.

`event-type` should be the AWS event source type that the subscription will be listening to. Please refer to [AAWS Event Types](https://docs.aws.amazon.com/AmazonCloudWatch/latest/events/EventTypes.html) for more details.

> **NOTE:** In subscription, `source-type` and `event-type` must match the CloudEvent attributes, otherwise Dispatch won't receive the event

