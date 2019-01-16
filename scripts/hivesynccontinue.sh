#!/bin/bash

POPULATE_CMD="$(which hive)"

HIVESYNC_PID=`pgrep -f 'hive sync'`

if [[ ! $? -eq 0 ]]; then
    echo NOTIFYALERT! hivemindsync quit unexpectedly, restarting hive sync...
    cd $APP_ROOT
    exec "${POPULATE_CMD}" sync 2>&1&
fi

sleep 30

HIVESERVER_PID=`pgrep -f 'hive server'`

if [[ ! $? -eq 0 ]]; then
    echo NOTIFYALERT! hivemindserver quit unexpectedly, restarting hive server...
    cd $APP_ROOT
    exec "${POPULATE_CMD}" server 2>&1&
fi

# prevent flapping
sleep 120