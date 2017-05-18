# Hive Checkpoints

`hive` will detect checkpoint files in this directory. They can be used to speed up core reindexing: it will check this directory on startup.

### Format

Files should be named `(block_num).json.lst` where `block_num` is the last block in the file.

The first file must begin with block 1. Successive files must begin with the previous file's block_num, plus 1.

e.g.:

 - 1000000.json.lst -- blocks 1 - 1,000,000
 - 2000000.json.lst -- blocks 1,000,001 - 2,000,000
 - 3000000.json.lst -- blocks 2,000,001 - 3,000,000

The intervals do not need to be regular, but blocks *must* be successive and there must be no duplicates.
