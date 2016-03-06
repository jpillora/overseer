#!/bin/bash

#NOTE: DONT CTRL+C OR CLEANUP WONT OCCUR
#      ENSURE PORTS 5001,5002 ARE UNUSED

#http file server
go get github.com/jpillora/serve
serve --port 5002 --quiet . &
SERVEPID=$!

#initial build
go build -ldflags '-X main.BUILD_ID=1' -o myapp
echo "BUILT APP (1)"
#run!
echo "RUNNING APP"
./myapp &
APPPID=$!

sleep 1
curl localhost:5001
sleep 1
curl localhost:5001
sleep 1
#request during an update
curl localhost:5001?d=5s &

go build -ldflags '-X main.BUILD_ID=2' -o myappnew
echo "BUILT APP (2)"

sleep 2
curl localhost:5001
sleep 1
curl localhost:5001
sleep 1
#request during an update
curl localhost:5001?d=5s &

go build -ldflags '-X main.BUILD_ID=3' -o myappnew
echo "BUILT APP (3)"

sleep 2
curl localhost:5001
sleep 1
curl localhost:5001
sleep 1
curl localhost:5001

sleep 1

#end demo - cleanup
kill $SERVEPID
kill $APPPID
rm myapp* 2> /dev/null

# Expected output:
# serving . on port 4000
# BUILT APP (1)
# RUNNING APP
# app#1 (96015cccdebcec119adad34f49b93e02552f3ad9) listening...
# app#1 (96015cccdebcec119adad34f49b93e02552f3ad9) says hello
# app#1 (96015cccdebcec119adad34f49b93e02552f3ad9) says hello
# BUILT APP (2)
# app#2 (ccc073a1c8e94fd4f2d76ebefb2bbc96790cb795) listening...
# app#2 (ccc073a1c8e94fd4f2d76ebefb2bbc96790cb795) says hello
# app#2 (ccc073a1c8e94fd4f2d76ebefb2bbc96790cb795) says hello
# app#1 (96015cccdebcec119adad34f49b93e02552f3ad9) says hello
# app#1 (96015cccdebcec119adad34f49b93e02552f3ad9) exiting...
# BUILT APP (3)
# app#3 (286848c2aefcd3f7321a65b5e4efae987fb17911) listening...
# app#3 (286848c2aefcd3f7321a65b5e4efae987fb17911) says hello
# app#3 (286848c2aefcd3f7321a65b5e4efae987fb17911) says hello
# app#2 (ccc073a1c8e94fd4f2d76ebefb2bbc96790cb795) says hello
# app#2 (ccc073a1c8e94fd4f2d76ebefb2bbc96790cb795) exiting...
# app#3 (286848c2aefcd3f7321a65b5e4efae987fb17911) says hello
# app#3 (286848c2aefcd3f7321a65b5e4efae987fb17911) exiting...
