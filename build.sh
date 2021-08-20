#!/bin/bash

GOOS=linux go build -o build/gumby -trimpath .
GOOS=linux go build -o build/importer -trimpath ./importer
