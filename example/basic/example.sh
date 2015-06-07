#!/bin/bash

#NOTE: DONT CTRL+C OR CLEANUP WONT OCCUR

#upgrade server (any http server)
# go get github.com/jpillora/serve
serve &

#initial build
echo "BUILDING APP 0.3.0"
go build -ldflags "-X main.VERSION 0.3.0" -o myapp

#run!
echo "RUNNING APP"
./myapp &

sleep 3

echo "BUILDING APP 0.3.1"
go build -ldflags "-X main.VERSION 0.3.1" -o myapp.0.3.1

sleep 4

echo "BUILDING APP 0.4.0"
go build -ldflags "-X main.VERSION 0.4.0" -o myapp.0.4.0

sleep 4

#end demo - cleanup
killall serve
killall myapp
rm myapp* 2> /dev/null