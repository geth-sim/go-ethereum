#!/bin/bash

# Flush file system buffers to disk
sync

# Drop page cache, dentries, and inodes
sudo sh -c "echo 3 > /proc/sys/vm/drop_caches"

echo "[INFO] Page cache, dentries, and inodes dropped."

