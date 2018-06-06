Here are some results when running server and client on the same machine (which is not good pattern to benchmark things actually). Numbers here are not scientific but can be useful to compare different transport performance.  

Setup:

* DigitalOcean droplet with 32 vCPU (Intel(R) Xeon(R) Platinum 8168 CPU @ 2.70GHz):
* Debian 9.4
* Message size 128 byte

```bash
apt-get update
apt-get -y install git
apt-get -y install curl
apt-get -y install htop
curl -O https://storage.googleapis.com/golang/go1.10.2.linux-amd64.tar.gz
tar xvf go1.10.2.linux-amd64.tar.gz
chown -R root:root ./go
mv go /usr/local
export GOPATH=$HOME/go
export PATH=$PATH:/usr/local/go/bin:$GOPATH/bin
go get -u github.com/centrifugal/centrifuge
go get -u github.com/centrifugal/centrifuge-go
```

Websocket with Protobuf proto:

```
$ go run main.go -s ws://localhost:8000/connection/websocket?format=protobuf -n 10000 -ns 100 -np 4 channel
Starting benchmark [msgs=10000, msgsize=128, pubs=4, subs=100]
Centrifuge Pub/Sub stats: 503,660 msgs/sec ~ 61.48 MB/sec
 Pub stats: 5,014 msgs/sec ~ 626.77 KB/sec
  [1] 1,294 msgs/sec ~ 161.80 KB/sec (2500 msgs)
  [2] 1,265 msgs/sec ~ 158.21 KB/sec (2500 msgs)
  [3] 1,254 msgs/sec ~ 156.79 KB/sec (2500 msgs)
  [4] 1,253 msgs/sec ~ 156.69 KB/sec (2500 msgs)
  min 1,253 | avg 1,266 | max 1,294 | stddev 16 msgs
 Sub stats: 498,673 msgs/sec ~ 60.87 MB/sec
  [1] 5,009 msgs/sec ~ 626.19 KB/sec (10000 msgs)
  ...
  [100] 5,001 msgs/sec ~ 625.19 KB/sec (10000 msgs)
  min 4,987 | avg 5,004 | max 5,012 | stddev 4 msgs
```

Websocket with JSON proto:

```
$ go run main.go -s ws://localhost:8000/connection/websocket -n 10000 -ns 100 -np 4 channel
Starting benchmark [msgs=10000, msgsize=128, pubs=4, subs=100]
Centrifuge Pub/Sub stats: 272,923 msgs/sec ~ 33.32 MB/sec
 Pub stats: 2,709 msgs/sec ~ 338.71 KB/sec
  [1] 687 msgs/sec ~ 85.89 KB/sec (2500 msgs)
  [2] 686 msgs/sec ~ 85.81 KB/sec (2500 msgs)
  [3] 682 msgs/sec ~ 85.34 KB/sec (2500 msgs)
  [4] 677 msgs/sec ~ 84.68 KB/sec (2500 msgs)
  min 677 | avg 683 | max 687 | stddev 3 msgs
 Sub stats: 270,221 msgs/sec ~ 32.99 MB/sec
  [1] 2,702 msgs/sec ~ 337.86 KB/sec (10000 msgs)
  ...
  [100] 2,704 msgs/sec ~ 338.04 KB/sec (10000 msgs)
  min 2,702 | avg 2,704 | max 2,709 | stddev 2 msgs
```

GRPC:

```
$ go run main.go -s grpc://localhost:8001 -n 10000 -ns 100 -np 4 channel
Starting benchmark [msgs=10000, msgsize=128, pubs=4, subs=100]
Centrifuge Pub/Sub stats: 468,781 msgs/sec ~ 57.22 MB/sec
 Pub stats: 4,787 msgs/sec ~ 598.48 KB/sec
  [1] 1,209 msgs/sec ~ 151.13 KB/sec (2500 msgs)
  [2] 1,212 msgs/sec ~ 151.51 KB/sec (2500 msgs)
  [3] 1,201 msgs/sec ~ 150.20 KB/sec (2500 msgs)
  [4] 1,196 msgs/sec ~ 149.62 KB/sec (2500 msgs)
  min 1,196 | avg 1,204 | max 1,212 | stddev 6 msgs
 Sub stats: 464,140 msgs/sec ~ 56.66 MB/sec
  [1] 4,783 msgs/sec ~ 597.95 KB/sec (10000 msgs)
  ...
  [100] 4,642 msgs/sec ~ 580.30 KB/sec (10000 msgs)
  min 4,642 | avg 4,761 | max 4,786 | stddev 45 msgs
```

A bit more subscribers for GRPC (1000)

```
$ go run main.go -s grpc://localhost:8001 -n 100000 -ns 1000 -np 4 channel
Starting benchmark [msgs=100000, msgsize=128, pubs=4, subs=1000]
Centrifuge Pub/Sub stats: 540,991 msgs/sec ~ 66.04 MB/sec
 Pub stats: 549 msgs/sec ~ 68.73 KB/sec
  [1] 137 msgs/sec ~ 17.19 KB/sec (25000 msgs)
  [2] 137 msgs/sec ~ 17.19 KB/sec (25000 msgs)
  [3] 137 msgs/sec ~ 17.19 KB/sec (25000 msgs)
  [4] 137 msgs/sec ~ 17.18 KB/sec (25000 msgs)
  min 137 | avg 137 | max 137 | stddev 0 msgs
 Sub stats: 540,450 msgs/sec ~ 65.97 MB/sec
  [1] 540 msgs/sec ~ 67.60 KB/sec (100000 msgs)
  ...
  [1000] 543 msgs/sec ~ 67.95 KB/sec (100000 msgs)
  min 540 | avg 541 | max 549 | stddev 1 msgs
```

More subscribers (1000) for Websocket protobuf:

```
$ go run main.go -s ws://localhost:8000/connection/websocket?format=protobuf -n 100000 -ns 1000 -np 4 channel
Starting benchmark [msgs=100000, msgsize=128, pubs=4, subs=1000]

Centrifuge Pub/Sub stats: 681,212 msgs/sec ~ 83.16 MB/sec
 Pub stats: 685 msgs/sec ~ 85.69 KB/sec
  [1] 172 msgs/sec ~ 21.57 KB/sec (25000 msgs)
  [2] 171 msgs/sec ~ 21.47 KB/sec (25000 msgs)
  [3] 171 msgs/sec ~ 21.42 KB/sec (25000 msgs)
  [4] 171 msgs/sec ~ 21.42 KB/sec (25000 msgs)
  min 171 | avg 171 | max 172 | stddev 0 msgs
 Sub stats: 680,531 msgs/sec ~ 83.07 MB/sec
  [1] 681 msgs/sec ~ 85.14 KB/sec (100000 msgs)
  ...
  [1000] 681 msgs/sec ~ 85.13 KB/sec (100000 msgs)
  min 680 | avg 680 | max 685 | stddev 1 msgs
```

And for JSON with 1000 subscribers:

```
$ go run main.go -s ws://localhost:8000/connection/websocket -n 1000 -ns 1000 -np 4 channel
Starting benchmark [msgs=1000, msgsize=128, pubs=4, subs=1000]
Centrifuge Pub/Sub stats: 265,900 msgs/sec ~ 32.46 MB/sec
 Pub stats: 278 msgs/sec ~ 34.85 KB/sec
  [1] 73 msgs/sec ~ 9.22 KB/sec (250 msgs)
  [2] 71 msgs/sec ~ 9.00 KB/sec (250 msgs)
  [3] 71 msgs/sec ~ 8.90 KB/sec (250 msgs)
  [4] 69 msgs/sec ~ 8.71 KB/sec (250 msgs)
  min 69 | avg 71 | max 73 | stddev 1 msgs
 Sub stats: 265,635 msgs/sec ~ 32.43 MB/sec
  [1] 273 msgs/sec ~ 34.16 KB/sec (1000 msgs)
  ...
  [1000] 277 msgs/sec ~ 34.67 KB/sec (1000 msgs)
  min 265 | avg 275 | max 278 | stddev 2 msgs
```

Websocket with 10k subscribers:

```
$ go run main.go -s ws://localhost:8000/connection/websocket?format=protobuf -n 10000 -ns 10000 -np 4 channel
Starting benchmark [msgs=10000, msgsize=128, pubs=4, subs=10000]
Centrifuge Pub/Sub stats: 504,496 msgs/sec ~ 61.58 MB/sec
 Pub stats: 55 msgs/sec ~ 6.89 KB/sec
  [1] 13 msgs/sec ~ 1.73 KB/sec (2500 msgs)
  [2] 13 msgs/sec ~ 1.72 KB/sec (2500 msgs)
  [3] 13 msgs/sec ~ 1.72 KB/sec (2500 msgs)
  [4] 13 msgs/sec ~ 1.72 KB/sec (2500 msgs)
  min 13 | avg 13 | max 13 | stddev 0 msgs
 Sub stats: 504,446 msgs/sec ~ 61.58 MB/sec
  [1] 50 msgs/sec ~ 6.32 KB/sec (10000 msgs)
  ...
  [10000] 50 msgs/sec ~ 6.34 KB/sec (10000 msgs)
  min 50 | avg 50 | max 55 | stddev 0 msgs
```

Publish without subscribers - i.e. measure publish rate:

```
$ go run main.go -s ws://localhost:8000/connection/websocket?format=protobuf -n 10000000 -ns 0 -np 32 channel
Starting benchmark [msgs=10000000, msgsize=128, pubs=32, subs=0]
Pub stats: 159,512 msgs/sec ~ 19.47 MB/sec
 [1] 5,010 msgs/sec ~ 626.33 KB/sec (312500 msgs)
  ...
 [32] 4,985 msgs/sec ~ 623.22 KB/sec (312500 msgs)
 min 4,985 | avg 4,993 | max 5,010 | stddev 5 msgs
```
