## Dispatch Event Driver for AWS
This repo contains a template for aws SQS event driver support for Dispatch.

### 1. Get AWS credentials
Follow [`Get AWS Access Key`](https://docs.aws.amazon.com/sdk-for-go/v1/developer-guide/setting-up.html) to obtain aws credential. Modify `<ACCESS_KEY_ID>` and `<SECRET_ACCESS_KEY>` in **credentials** file under this repo:
```
[default]
aws_access_key_id = <ACCESS_KEY_ID>
aws_secret_access_key = <SECRET_ACCESS_KEY>
```

### 2. Create AWS SQS Queue *[will be managed by Dispatch]*
Create a standard AWS SQS with name *dispatch*, and also enable `long-polling` by setting `Receive Message Wait Time` attribute to 20.

For more details, refer to [SQS long-polling](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-long-polling.html)


### 3. Create Rules in AWS CloudWatch *[will be managed by Dispatch]*
In *AWS console* -> *CloudWatch* -> *Event* -> *Rules* -> *Create Rules* for the event that want to be received by Dispatch. Choose *AWS Console Sign-in* -> *Sign-in Events* for example

For Event Source, take a note of the `source` field of the chosen event, this will be used in creation of Dispatch subscription later.
> **Note:** Most aws events display `source` field under *Event Patter Preview* section, if not, please expand *Show sample events* and look for `source` field.

For Event Targets, choose the SQS queue *dispatch* created in step 2.


### 4. Build Event Driver Image
```bash
$ docker build -t dispatch-events-aws .
```

### 5. Create Eventdrivertype in Dispatch
```bash
$ dispatch create eventdrivertype aws-sqs dispatch-events-aws:latest
Created event driver type: aws-sqs
```

### 6. Create Eventdriver in Dispatch
```bash
$ dispatch create eventdriver aws-sqs
Created event driver: holy-grackle-805996
```

### 7. Create Subscription in Dispatch

```bash
$ dispatch create subscription hello-py --event-type="aws.signin" --source-type="aws"
created subscription: innocent-werewolf-420270
```

`source-type` should be `aws`.

`event-type` should be the AWS event source type that the subscription will be listening to. This attribute can be found in AWS Console -> CloudWatch -> Events -> Rules -> Create Rules -> Event Source -> Event Pattern -> `source` attribute in event pattern preview.

> **NOTE:** In subscription, `source-type` and `event-type` must match the CloudEvent attributes

