#!/bin/bash

cp $GOPATH/src/github.com/centrifugal/centrifuge/misc/proto/client.gogo.proto
cd internal/proto && protoc --proto_path=$GOPATH/src:$GOPATH/src/github.com/centrifugal/centrifuge-go/vendor:. --gogofaster_out=plugins=grpc:. client.proto
