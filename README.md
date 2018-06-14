## Dispatch Event Driver for AWS - Twitter Example
Implementation of AWS Twitter example for Dispatch.

Workflows:
User uploads image to s3 bucket -> Dispatch eventdriver fetches the cloud event and sends it to Dispatch -> Dispatch executes function with the event as payload -> Function watermarks the uploaded image with Dispatch logo and tweets the image to Twitter.

Steps:
1. [Setup Event Driver](#eventdriver-setup)
2. [Setup Dispatch Function](#function-setup)
3. [Create Subscription](#create-subscription)
4. [Upload Images](#upload-image-to-s3-bucket)

## Eventdriver Setup
### Get AWS credentials
Follow [`Get AWS Access Key`](https://docs.aws.amazon.com/sdk-for-go/v1/developer-guide/setting-up.html) to obtain aws region and credential, take a note for `<aws_region>`, `<aws_access_key_id>` and `<aws_secret_access_key>`. Then create a secret file:
```bash
$ cat << EOF > aws-secret.json
{
    "aws_access_key_id": "${AWS_AKID}",
    "aws_secret_access_key": "${AWS_SECRET_KEY}"
}
EOF
```
Next, create a Dispatch secret which contains aws credentials:
```bash
$ dispatch create secret aws aws-secret.json
Created secret: aws
```

> **NOTE**: AWS user for this example should have following permissions:
> * Create CloudTrail (create/delete)
> * Create S3 Bucket (create bucket, put/get object)
> * Create CloudWatch Rule (create/delete)
> * Create SQS (create/delete)
> * [TODO]


### Build Event Driver Image
```bash
$ docker build -t dispatch-events-aws .
```

### Create Eventdrivertype in Dispatch
Create a Dispatch eventdrivertype with name *aws*:
```bash
$ dispatch create eventdrivertype aws dispatch-events-aws:latest
Created event driver type: aws
```

### Create Eventdriver in Dispatch
When creating AWS eventdriver, following parameters should be specified properly:
* `region`: obtained from step 1, default *us-west-2*
* `bucket-name` : specifying S3 bucket name for storing log file and triggering object-level events

Following parameters are optional (have defaults):
* `queue-name` : specifying SQS queue name prefix, default "*dispatch*"
* `duration` : specifying SQS polling message duration, default "*20*" (0-20 seconds), which enables [Long-polling](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-long-polling.html)
* `namespace` : specifying event namespace, default "*dispatchframework.io/aws-event*"
* `source-id` : specifying event source id, default a new UUID string
* `clean-up` : clean up AWS resources after Driver shuts down. default *false*
* `event-patterns` : a json object array string containing all additional AWS event patterns. For more details: (https://docs.aws.amazon.com/AmazonCloudWatch/latest/events/CloudWatchEventsandEventPatterns.html) . An example event-patterns can be found in *event-patterns-example.json* file.

All parameters should be configued through `--set` flag. Create a event driver using:
```bash
$ dispatch create eventdriver aws --secret aws --set clean-up --set bucket-name=dispatch-events-aws-twitter-example
```

> **NOTE:** The first s3 bucket event might come with a 3~5 minutes delay. After that, events will be catched properly.


## Function Setup
### Create Twitter App Credentials
Get Twitter app secrets and put them in `twitter-secret.json`,
```json
{
    "appKey": "<Consumer Key (API Key)>",
    "appSecret": "<Consumer Secret (API Secret)>",
    "oathToken": "<Access Token>",
    "oathTokenSecret": "<Access Token Secret>"
}
```
Create Dispatch secret for twitter:
```bash
$ dispatch create secret twitter twitter-secret.json
Created secret: twitter
```

### Create Base Image and Image
Create base-image:
```bash
$ dispatch create base-image python3-base dispatchframework/python3-base:0.0.7 --language python3
```
After base-image is ready (check by `dispatch get base-image`), create image:
```bash
$ dispatch create image python3-twitter python3-base --runtime-deps ./requirements.txt
```

### Create Function
Once base image and image are both ready, create function `twitter`:
```bash
$ dispatch create function twitter --image python3-twitter ./twitter.py --secret twitter --secret aws
```
Function will consume aws secret for downloading file and twitter secret for tweeting image.


## Create Subscription
To make events from eventdriver be processed by Dispatch, the next step is to create Dispatch subscription which sends events to a function. (the function name created in step 1 is `twitter`)
```bash
$ dispatch create subscription twitter --event-type="aws.s3" --source-type="aws"
created subscription: innocent-werewolf-420270
```

`source-type` should be `aws`.

`event-type` should be the AWS event source type that the subscription will be listening to. For this s3 example, should be `aws.s3`. Please refer to [AWS Event Types](https://docs.aws.amazon.com/AmazonCloudWatch/latest/events/EventTypes.html) for more details.

> **NOTE:** In subscription, `source-type` and `event-type` must match the CloudEvent attributes, otherwise Dispatch won't invoke the function.


## Upload Image to S3 Bucket

Upload a image to `images` folder inside the s3 bucket, which was created during event driver creation. Then, a watermarked image should be tweeted.

> TODO:
> * make target folder configurable
> * consider images access, grantees

