#!/bin/bash

set -e

if [ "$1" == "" ]; then echo Please pass a docset name as an argument.; exit 1; fi

[ -d wscat ] || git clone https://github.com/websockets/wscat.git
cd wscat ; npm i ; cd ..

ID=$(curl http://localhost:12340/repo/1/items | jq -r ".[] | select(.name==\"$1\") | .id, .title")

if [ "$ID" == "" ]; then 
	echo $1 not found. Available Docsets:
	curl http://localhost:12340/repo/1/items | jq -r ".[] | .name" | sort
	echo Aborting because docset was not found.
	exit 1;
fi

TITLE="$(echo $ID | cut -d ' ' -f 2-)"
ID=$(echo $ID | cut -d ' ' -f 1)
echo Found $1 with id = $ID, title = $TITLE. Downloading...
curl -vvv http://localhost:12340/item -d '{"id": "'$ID'"}' &

node wscat/bin/wscat -o http://localhost/ --connect ws://localhost:12340/download_progress

echo "Success!"
