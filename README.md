# Hivemind

#### Developer-friendly microservice powering social networks on the Steem blockchain.

Hive is a "consensus interpretation" layer for the Steem blockchain, maintaining the state of social features such as post feeds, follows, and communities. Written in Python, it synchronizes an SQL database with chain state, providing developers with a more flexible/extensible alternative to the raw `steemd` API.


## Development Environment

```
$ brew install python3 postgresql
$ createdb hive
$ export DATABASE_URL=postgresql://user:pass@localhost:5432/hive

$ git clone https://github.com/steemit/hivemind.git
$ cd hivemind
$ pip3 install -e .

$ hive sync
```

To start the server:

```
$ hive server
```

##### Check sync status:

```
$ hive status
{'db_head_block': 19930833, 'db_head_time': '2018-02-16 21:37:36', 'db_head_age': 10}
```

Or curl the API:

```
$ curl --data '{"jsonrpc":"2.0","id":0,"method":"db_head_state"}' http://localhost:8080
{"jsonrpc": "2.0", "result": {"db_head_block": 19930795, "db_head_time": "2018-02-16 21:35:42", "db_head_age": 10}, "id": 0}
```


## Production Environment

Hive is deployed as Docker container (see `Dockerfile`).


## Configuration

**Precedence:** run-time args > environment variables > `hive.conf`

| ENV                      | arg                  | Default |
| ------------------------ | -------------------- | ------- |
| `LOG_LEVEL`              | `--log-level`        | INFO    |
| `HTTP_SERVER_PORT`       | `--http-server-port` | 8080    |
| `DATABASE_URL`           | `--database-url`     | postgresql://user:pass@localhost:5432/hive |
| `STEEMD_URL`             | `--steemd-url`       | https://api.steemit.com |
| `MAX_BATCH`              | `--max-batch`        | 200     |
| `TRAIL_BLOCKS`           | `--trail-blocks`     | 2       |

## Requirements



### Hardware

 - Focus on Postgres performance
 - Hive requires ~2GB of memory (TODO: verify/limit max usage during initial sync)


### Steem config

##### Build flags

 - `LOW_MEMORY_MODE=OFF` - needs post content
 - `CLEAR_VOTES=OFF` - needs all vote data
 - `SKIP_BY_TX=ON` - tx id lookup not necessary

##### Plugins

 - `follow` - for reputation data (to be replaced with [reputation](https://github.com/steemit/steem/issues/1425))
 - `witness` - for account activity/bandwidth data
 - Not required: `tags`, `market_history`, `account_history`


### Postgres Performance

For a system with 32G of memory, here's a good start:

```
effective_cache_size = 12GB # 50-75% of avail memory
maintenance_work_mem = 2GB
random_page_cost = 1.0      # assuming SSD storage
shared_buffers = 4GB        # 25% of memory
work_mem = 512MB
synchronous_commit = off
checkpoint_completion_target = 0.9
```

## Documentation

```
$ pip install pdoc
$ make docs
$ open docs/hive/index.html
```

## License

MIT
