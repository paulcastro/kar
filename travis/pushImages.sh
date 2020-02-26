#!/bin/bash

set -eux

SCRIPTDIR=$(cd $(dirname "$0") && pwd)
ROOTDIR="$SCRIPTDIR/../"

IMAGE_TAG=$1
cd $ROOTDIR

docker login -u iamapikey -p "${DOCKER_APIKEY}" us.icr.io

DOCKER_IMAGE_TAG=${IMAGE_TAG} make dockerPush

# if doing nightly also push a tag with the hash commit
if [ ${IMAGE_TAG} == "nightly" ]; then
  SHORT_COMMIT=`git rev-parse --short HEAD`
  DOCKER_IMAGE_TAG=dev-${SHORT_COMMIT} make dockerPush
fi