@echo off
cd ..\\
go run main.go -client=0 -socks=false -logtype=netlogger -latencytest=true
pause