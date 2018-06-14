"""
* secret
dispatch create secret twitter ./twitter-secret.json

* image
dispatch create base-image python3-base dispatchframework/python3-base:0.0.7 --language python3
dispatch create image python3-twitter python3-base --runtime-deps ./requirements.txt

* function:
dispatch create function twitter --image python3-twitter ./twitter.py --secret twitter-credential
"""

import io
import json
import requests
import boto3
from PIL import Image
from twython import Twython

def get_image(url):
    r = requests.get(url)
    if r.status_code == 200:
        return Image.open(io.BytesIO(r.content))


def get_s3_image(ctx, payload):
    secrets = ctx["secrets"]
    access_key=secrets["aws_access_key_id"]
    secret_key=secrets["aws_secret_access_key"]

    s3_client = boto3.client(
        's3',
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
    )

    event = payload["detail"]
    key = event["requestParameters"]["key"]
    bucket = event["requestParameters"]["bucketName"]

    imgBytes = io.BytesIO()
    s3_client.download_fileobj(bucket, key, imgBytes)
    img = Image.open(imgBytes)

    img.thumbnail((1024, 512), Image.BICUBIC)
    return img


def get_watermark_image():
    watermark_url = "https://github.com/dispatchframework/cloudevents-twitter-demo/raw/master/logo-small-watermark.png"
    img = get_image(watermark_url)
    img.thumbnail((256, 256))
    return img


def add_watermark(img, watermark):
    # Watermark the image
    img.paste(watermark, (0, 0), watermark)

    # Save into a byte stream
    final_image = io.BytesIO()
    img.save(final_image, format="PNG")
    final_image.seek(0)
    return final_image


def twitter_image(ctx, image):
    secrets = ctx["secrets"]
    appKey = secrets["appKey"]
    appSecret = secrets["appSecret"]
    oathToken = secrets["oathToken"]
    oathTokenSecret = secrets["oathTokenSecret"]

    twitter = Twython(appKey, appSecret, oathToken, oathTokenSecret)
    response = twitter.upload_media(media=image)
    twitter.update_status(status='Upload from VMware Dispatch',
                          media_ids=[response['media_id']])


def handle(ctx, payload):
    img = get_s3_image(ctx, payload)
    watermark = get_watermark_image()

    final_image = add_watermark(img, watermark)

    twitter_image(ctx, final_image)

    return {"status": 200}
