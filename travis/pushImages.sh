#!/bin/bash

set -eu

SCRIPTDIR=$(cd $(dirname "$0") && pwd)
ROOTDIR="$SCRIPTDIR/.."

BRANCH=$1
IMAGE_TAG=$2
cd $ROOTDIR

docker login -u iamapikey -p "${DOCKER_APIKEY_RIS}" us.icr.io

SHORT_COMMIT=`git rev-parse --short HEAD`

if [ ${BRANCH} == "master" ] && [ ${IMAGE_TAG} == "latest" ]; then
    # push commit hash tagged images
    DOCKER_NAMESPACE=research/kar-dev DOCKER_IMAGE_TAG=dev-${SHORT_COMMIT} make dockerBuildAndPush

    # push `latest` tag images
    DOCKER_NAMESPACE=research/kar-dev DOCKER_IMAGE_TAG=latest make dockerBuildAndPush
else
    if [ ${BRANCH} == ${IMAGE_TAG} ]; then
        # A git tag operation, push to kar-prod
        DOCKER_NAMESPACE=research/kar-prod DOCKER_IMAGE_TAG=${IMAGE_TAG} make dockerBuildAndPush
    else
        # A push to some branch; push commit-taged image to kar-stage
        DOCKER_NAMESPACE=research/kar-stage DOCKER_IMAGE_TAG=${BRANCH}-${SHORT_COMMIT} make dockerBuildAndPush
    fi
fi