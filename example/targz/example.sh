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

echo "BUILDING AND ARCHIVING APP 0.4.0"
gox -osarch "darwin/amd64" -ldflags "-X main.VERSION 0.4.0" -output "myapp_{{.OS}}_{{.Arch}}_0.4.0"
for f in myapp_*; do
	tar czvf $f.tar.gz l.txt $f n.txt
	rm $f
done

sleep 10

#end demo - cleanup
killall serve
killall myapp
rm myapp* 2> /dev/null