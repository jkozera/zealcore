#!/bin/bash

if [ "$1" == "" ]; then echo Please pass a docset name as an argument.; exit 1; fi

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

while [ "$(jobs -pr)" != "" ]; do
	echo "Progress update - waiting for GET to finish (proper progress reporting mechanism TBD):"
	ls -lh "$TITLE.zealdocset"
	echo
	sleep 2s
done

echo "Success! Now restart zealcore to be able to search this docset."
