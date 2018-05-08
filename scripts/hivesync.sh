#!/bin/bash

# if the indexer dies by itself, kill runsv causing the container to exit
HIVESYNC_PID=`pgrep -f 'hive sync'`
if [[ ! $? -eq 0 ]]; then
    echo NOTIFYALERT! hivemindsync has quit unexpectedly, killing container and starting a new instance..
    sleep 30
    RUN_SV_PID=`pgrep -f /etc/service/hivesync`
    kill -9 $RUN_SV_PID
fi

# NOTE: this API endpoint returns head age in seconds, and a 200 code if it's less than 3600
HTTP_CODE=`curl -I -s -o /dev/null -w "%{http_code}" http://127.0.0.1/head_age`

# if we get a 200 then hive is synced, start syncing operation
if [[ ${HTTP_CODE} -eq 200 ]]; then
    kill -SIGINT $HIVESYNC_PID
    echo hivemindsync: waiting for hive to exit cleanly
    while [ -e /proc/$HIVESYNC_PID ]; do sleep 0.1; done
    echo hivemindsync: stopping postgres service
    service postgresql stop
    echo hivemindsync: starting a new state file upload operation, compressing directory...
    cd /var/lib/postgresql/9.5
    tar cf hivemind.tar.bz2 --use-compress-prog=pbzip2 -C /var/lib/postgresql/9.5 main
    if [[ ! $? -eq 0 ]]; then
      echo NOTIFYALERT! hivemindsync was unable to compress shared memory file, check the logs.
      exit 1
    fi
    FILE_NAME=hivemind-$SCHEMA_HASH-`date '+%Y%m%d-%H%M%S'`.tar.bz2
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
    RUN_SV_PID=`pgrep -f /etc/service/hivesync`
    kill -9 $RUN_SV_PID
fi

# check every 60 seconds if synced
sleep 60
