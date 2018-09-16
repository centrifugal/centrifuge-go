#!/bin/bash
cp $GOPATH/src/github.com/centrifugal/centrifuge/misc/proto/client.gogo.proto internal/proto/client.proto
cd internal/proto && protoc --proto_path=$GOPATH/src:$GOPATH/src/github.com/centrifugal/centrifuge-go/vendor:. --gogofaster_out=. client.proto
