#!/bin/bash

#NOTE: DONT CTRL+C OR CLEANUP WONT OCCUR
#      ENSURE PORTS 5001,5002 ARE UNUSED

#initial build
go build -ldflags '-X main.BuildID=1' -o my_app
echo "BUILT APP (1)"
#run!
echo "RUNNING APP"
./my_app &
APPPID=$!

sleep 1
curl localhost:5001
sleep 1
curl localhost:5001
sleep 1
#request during an update
curl localhost:5001?d=5s &

go build -ldflags '-X main.BuildID=2' -o my_app_next
echo "BUILT APP (2)"

sleep 2
curl localhost:5001
sleep 1
curl localhost:5001
sleep 1
#request during an update
curl localhost:5001?d=5s &

go build -ldflags '-X main.BuildID=3' -o my_app_next
echo "BUILT APP (3)"

sleep 2
curl localhost:5001
sleep 1
curl localhost:5001
sleep 1
curl localhost:5001

sleep 1

#end demo - cleanup
kill $APPPID
rm my_app* 2> /dev/null

# Expected output (hashes will vary across OS/arch/go-versions):
# BUILT APP (1)
# RUNNING APP
# app#1 (9ba12be7d6f581835c6947845aa742cc05515365) listening...
# app#1 (9ba12be7d6f581835c6947845aa742cc05515365) says hello
# app#1 (9ba12be7d6f581835c6947845aa742cc05515365) says hello
# BUILT APP (2)
# app#2 (180d6284b53f9618b92a2a4c0450521c93d767b7) listening...
# app#2 (180d6284b53f9618b92a2a4c0450521c93d767b7) says hello
# app#2 (180d6284b53f9618b92a2a4c0450521c93d767b7) says hello
# app#1 (9ba12be7d6f581835c6947845aa742cc05515365) says hello
# app#1 (9ba12be7d6f581835c6947845aa742cc05515365) exiting...
# BUILT APP (3)
# app#3 (df4f68714724a856d24a08e44102fe41bbf9ee9f) listening...
# app#3 (df4f68714724a856d24a08e44102fe41bbf9ee9f) says hello
# app#3 (df4f68714724a856d24a08e44102fe41bbf9ee9f) says hello
# app#3 (df4f68714724a856d24a08e44102fe41bbf9ee9f) says hello
# app#2 (180d6284b53f9618b92a2a4c0450521c93d767b7) says hello
# app#2 (180d6284b53f9618b92a2a4c0450521c93d767b7) exiting...
