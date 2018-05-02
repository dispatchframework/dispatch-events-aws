## Dispatch Event Driver for AWS
This repo implements AWS event driver for Dispatch.

### 1. Get AWS credentials
Follow [`Get AWS Access Key`](https://docs.aws.amazon.com/sdk-for-go/v1/developer-guide/setting-up.html) to obtain aws region and credential, take a note for `<aws_region>`, `<aws_access_key_id>` and `<aws_secret_access_key>`. Create env varbiable for these two fields:
```bash
$ export AWS_AKID=<aws_access_key_id>
$ export AWS_SECRET_KEY=<aws_secret_access_key>
```
Then create a secret file:
```bash
$ cat << EOF > secret.json
{
    "aws_access_key_id": "${AWS_AKID}",
    "aws_secret_access_key": "${AWS_SECRET_KEY}"
}
EOF
```
Next, create a Dispatch secret which contains aws credentials:
```bash
$ dispatch create secret aws-credential secret.json
Created secret: aws-credential
```

### 2. Build Event Driver Image
```bash
$ docker build -t dispatch-events-aws .
```

### 3. Create Eventdrivertype in Dispatch
Create a Dispatch eventdrivertype with name *aws-sqs*:
```bash
$ dispatch create eventdrivertype aws-sqs dispatch-events-aws:latest
Created event driver type: aws-sqs
```

### 4. Create Eventdriver in Dispatch
When creating AWS eventdriver, following parameters should be specified properly:
* `region`: obtained from step 1, default *us-west-2*
* Two ways to invoke target: *Event Pattern* or *Schedule*:
  * `event-pattern` : setting up event pattern for AWS CloudWatch Rule, refer to [AWS Event Pattern](https://docs.aws.amazon.com/AmazonCloudWatch/latest/events/CloudWatchEventsandEventPatterns.html) for more details.
  * `schedule-expression` : specifying event schedule expression, please refer to [AWS Schedule Events](https://docs.aws.amazon.com/AmazonCloudWatch/latest/events/ScheduledEvents.html) for details.

Following parameters are optional (have defaults):
* `rule-name` : specifying CloudWatch Rule name, default "*dispatch*"
* `queue-name` : specifying SQS queue name, default "*dispatch*"
* `duration` : specifying SQS polling message duration, default "*20*" (0-20 seconds), which enables [Long-polling](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-long-polling.html)
* `namespace` : specifying event namespace, default "*dispatchframework.io/aws-event*"
* `source-id` : specifying event source id, default a new UUID string
* `clean-up` : clean up AWS resources after Driver shuts down. default *false*

All parameters should be configued through `--set` flag. For example, create a event driver using `event-pattern`:
```bash
$ dispatch create eventdriver aws-sqs --secret aws-credential --set region="us-west-2" --set event-pattern="{\"source\":[\"aws.autoscaling\"]}" --set clean-up
Created event driver: holy-grackle-805996
```

or using `schedule-expression`:
```bash
$ dispatch create eventdriver aws-sqs --secret aws-credential --set region="us-west-2" --set schedule-expression="rate(1 minute)" --set clean-up
Created event driver: trusting-marlin-576200
```


### 5. Create Subscription in Dispatch
To make events from eventdriver be processed by Dispatch, the last step is to create Dispatch subscription which sends events to a function. For example, to create a `aws.autoscaling` event subscription (bind to function hello-py):
```bash
$ dispatch create subscription hello-py --event-type="aws.autoscaling" --source-type="aws"
created subscription: innocent-werewolf-420270
```

`source-type` should be `aws`.

`event-type` should be the AWS event source type that the subscription will be listening to. Please refer to [AWS Event Types](https://docs.aws.amazon.com/AmazonCloudWatch/latest/events/EventTypes.html) for more details.

> **NOTE:** In subscription, `source-type` and `event-type` must match the CloudEvent attributes, otherwise Dispatch won't receive the event

