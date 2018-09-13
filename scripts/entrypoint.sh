#!/bin/bash

# NOTE: this script will be executed again if hive crashes or aborts. This
# could happen in the case of an unexpected upstream/API response, or when
# a non-micro-fork was encountered. Hive has a startup routine which attempts
# to recover automatically, so database should be kept intact between restarts.

# eb need to set: RUN_IN_EB, S3_BUCKET, SYNC_TO_S3 (boolean) if a syncer
# hive expects: DATABASE_URL, LOG_LEVEL, STEEMD_URL, JUSSI_URL
# default DATABASE_URL should be postgresql://postgres:postgres@localhost:5432/postgres

POPULATE_CMD="$(which hive)"

if [[ "$RUN_IN_EB" ]]; then
  mkdir /var/lib/postgresql/9.5/main
  if [[ $? -ne 0 ]]; then
    echo hivemind: restarted -- db already exists. skip init, start postgres
    service postgresql start
  else
    chown -R postgres:postgres /var/lib/postgresql/9.5
    cd /var/lib/postgresql/9.5

    echo hivemind: attempting to pull in state file from s3://$S3_BUCKET/hivemind-$SCHEMA_HASH-latest.tar.lz4

    finished=0
    count=1
    while [[ $count -le 5 ]] && [[ $finished == 0 ]]
    do
      s3cmd get s3://$S3_BUCKET/hivemind-$SCHEMA_HASH-latest.tar.lz4 - | lz4 -d | tar x
      if [[ $? -ne 0 ]]; then
        sleep 1
        echo notifyalert hivemind: unable to pull state from S3 - attempt $count
        (( count++ ))
      else
        finished=1
      fi
    done

    if [[ $finished == 0 ]]; then
      if [[ ! "$SYNC_TO_S3" ]]; then
        echo notifyalert hivemind: unable to pull state from S3 - exiting
        exit 1
      else
        echo hivemindsync: state file for schema version $SCHEMA_HASH not found, creating a new one from genesis
        chpst -upostgres /usr/lib/postgresql/9.5/bin/initdb -D /var/lib/postgresql/9.5/main
      fi
    else
      echo hivemind: state file loaded successfully
    fi

    service postgresql start

    # following config assumes 12GB mem available for pg
    chpst -upostgres psql -c "ALTER SYSTEM SET effective_cache_size = '7GB';"
    chpst -upostgres psql -c "ALTER SYSTEM SET maintenance_work_mem = '512MB';"
    chpst -upostgres psql -c "ALTER SYSTEM SET random_page_cost = 1.0;"
    chpst -upostgres psql -c "ALTER SYSTEM SET shared_buffers = '3GB';"
    chpst -upostgres psql -c "ALTER SYSTEM SET work_mem = '512MB';"
    chpst -upostgres psql -c "ALTER SYSTEM SET synchronous_commit = 'off';"
    chpst -upostgres psql -c "ALTER SYSTEM SET checkpoint_completion_target = 0.9;"
    chpst -upostgres psql -c "ALTER SYSTEM SET checkpoint_timeout = '30min';"
    chpst -upostgres psql -c "ALTER SYSTEM SET max_wal_size = '4GB';"

    chpst -upostgres psql -c "ALTER USER postgres WITH PASSWORD 'postgres';"

    service postgresql restart
  fi
fi

cd $APP_ROOT

# startup hive
echo hivemind: starting sync
exec "${POPULATE_CMD}" sync 2>&1&

echo hivemind: starting server
if [[ ! "$SYNC_TO_S3" ]]; then
    exec "${POPULATE_CMD}" server
else
    exec "${POPULATE_CMD}" server --log-level=warning 2>&1&
    mkdir -p /etc/service/hivesync
    cp /usr/local/bin/hivesync.sh /etc/service/hivesync/run
    chmod +x /etc/service/hivesync/run
    echo hivemind: starting hivesync service
    runsv /etc/service/hivesync
fi

echo hivemind: application has stopped, see log for errors
