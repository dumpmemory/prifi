@echo off
cd ..\\dissent\\
go run main.go config.go client.go relay.go relaySocks.go trusteeServer.go -relay -t1host=localhost:9000 -t2host=localhost:9000 -reportlimit=100
pause