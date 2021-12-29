#!/bin/bash

GOARCH=amd64 GOOS=linux go build -o build/gumby -trimpath .
GOARCH=amd64 GOOS=linux go build -o build/importer -trimpath ./importer
