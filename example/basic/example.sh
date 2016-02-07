#!/bin/bash

#NOTE: DONT CTRL+C OR CLEANUP WONT OCCUR

#binary hosting server (any file server)
# go get github.com/jpillora/serve
serve &

#initial build
echo "BUILDING APP: A"
go build -ldflags "-X main.FOO=A" -o myapp

#run!
echo "RUNNING APP"
./myapp &

sleep 3

echo "BUILDING APP: B"
go build -ldflags "-X main.FOO=B" -o newmyapp

sleep 4

echo "BUILDING APP: C"
go build -ldflags "-X main.FOO=C" -o newmyapp

sleep 4

#end demo - cleanup
killall serve
killall myapp
rm myapp* 2> /dev/null
