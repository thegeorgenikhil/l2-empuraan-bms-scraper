#!/bin/bash

TARGET_DIR="/tmp/rod/user-data/"

current_time=$(date "+%Y-%m-%d %H:%M:%S")

# Count directories and size before cleanup
before_count=$(find "$TARGET_DIR" -mindepth 1 -type d | wc -l)
before_size=$(du -sh "$TARGET_DIR" | awk '{print $1}')

# Perform cleanup: find and delete directories older than 5 minutes
find "$TARGET_DIR" -mindepth 1 -type d -mmin +5 -exec rm -rf {} +

# Count directories and size after cleanup
after_count=$(find "$TARGET_DIR" -mindepth 1 -type d | wc -l)
after_size=$(du -sh "$TARGET_DIR" | awk '{print $1}')

# Calculate deleted directories
deleted_count=$((before_count - after_count))

# Log the results
echo "[$current_time] Folders before: $before_count ($before_size), Folders after: $after_count ($after_size), Deleted: $deleted_count"
