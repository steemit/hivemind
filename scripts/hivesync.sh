#!/bin/bash

HIVESYNC_PID=`pgrep -f 'hive sync'`

if [[ ! $? -eq 0 ]]; then
    echo NOTIFYALERT! hivemindsync quit unexpectedly, saving partial state...
    FILE_NAME=hivemind-$SCHEMA_HASH-`date '+%Y%m%d-%H%M%S'`-partial.tar.bz2

else
    # returns 200 if head is < 1hr old, signalling sync is complete. else, 500.
    HTTP_CODE=`curl -I -s -o /dev/null -w "%{http_code}" http://127.0.0.1/head_age`
    if [[ ${HTTP_CODE} -eq 200 ]]; then
        echo hivemindsync: sync complete, waiting for hive to exit cleanly...
        kill -SIGINT $HIVESYNC_PID
        while [ -e /proc/$HIVESYNC_PID ]; do sleep 0.1; done
        FILE_NAME=hivemind-$SCHEMA_HASH-`date '+%Y%m%d-%H%M%S'`.tar.bz2
    else
        # hive is still syncing, not complete... check back in a minute
        sleep 60
        exit 0
    fi
fi

echo hivemindsync: stopping postgres service
service postgresql stop

echo hivemindsync: starting a new state file upload operation, compressing directory...
cd /var/lib/postgresql/9.5
tar cf hivemind.tar.bz2 --use-compress-prog=pbzip2 -C /var/lib/postgresql/9.5 main
if [[ ! $? -eq 0 ]]; then
  echo NOTIFYALERT! hivemindsync was unable to compress state file, check the logs.
  exit 1
fi

echo hivemindsync: uploading $FILE_NAME to s3://$S3_BUCKET
aws s3 cp hivemind.tar.bz2 s3://$S3_BUCKET/$FILE_NAME
if [[ ! $? -eq 0 ]]; then
    echo NOTIFYALERT! hivemindsync was unable to upload $FILE_NAME to s3://$S3_BUCKET
    exit 1
fi

echo hivemindsync: replacing current version of hivemind-$SCHEMA_HASH-latest.tar.bz2 with $FILE_NAME
aws s3 cp s3://$S3_BUCKET/$FILE_NAME s3://$S3_BUCKET/hivemind-$SCHEMA_HASH-latest.tar.bz2
if [[ ! $? -eq 0 ]]; then
    echo NOTIFYALERT! hivemindsync was unable to overwrite the current statefile with $FILE_NAME
    exit 1
fi

# kill the container starting the process again
echo hivemindsync: state upload complete, killing container and starting a new instance..
RUN_SV_PID=`pgrep -f /etc/service/hivesync`
kill -9 $RUN_SV_PID
