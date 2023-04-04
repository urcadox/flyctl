#!/bin/bash
num_files=$(jq '. | length' /home/runner/files.json)

for file in $(jq  '.[]' /home/runner/files.json | cut -d '"' -f 2); do
  if grep -q res.Body.Close() "$File"; then
    exit 1
  fi
done