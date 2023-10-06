#!/bin/bash
# Builds protobuf go files.
# You only need to run this if you change a .proto file

 
protoc \
  --go_out=. \
  --go_opt=paths=source_relative \
  proto/*.proto
