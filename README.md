## Hivemind
`hivemind` is an off-chain consensus layer for Steem communities and API server for social features like feeds and follows.

# Dev Environment

Prepare MySQL Database (once):
```
make mysql
```

Prepare and load REPL (on change):
```
make build
make iypthon
```

## Setting up MySQL
First, we need to setup a MySQL server. An easy way to do that (in Docker) is to run:
```
make mysql
```

Then we need to set `DATABASE_URL` environment variable, for example:
```
set DATABASE_URL 'mysql://root:root_password@mysql:3306/testdb'
```

Lastly we invoke the `ensure-schema` command to create MySQL tables.

```
hive db ensure-schema --yes
```

## Indexing the blockchain
We can index the blockchain using cli as well. 
```
hive indexer from-steemd
```

If we have a `.json.lst` file containing first X blocks, we can index from that (its much faster).
```
hive indexer from-file /path/to/blocks.json.lst
```

## Starting API Server
```
hive server dev-server --port 1234
```

## Spec
[Community Spec Draft](https://github.com/steemit/condenser/wiki/Community-Spec-%5BDRAFT%5D)

## License
MIT