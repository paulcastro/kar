#!/bin/bash

SCRIPTDIR=$(cd $(dirname "$0") && pwd)
CODEDIR="$SCRIPTDIR/.."

KAR_VERBOSE=${KAR_VERBOSE:="info"}

VERBOSE=1 kar -v $KAR_VERBOSE -app ykt -service simulation -actors Site,Floor,Office,Researcher node $CODEDIR/ykt.js