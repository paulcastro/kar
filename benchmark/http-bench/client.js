/*
 * Copyright IBM Corporation 2020,2021
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// retry http requests up to 10 times over 10s
const fetch = require('fetch-retry')(require('node-fetch'), { retries: 10 })

// request url for a given KAR service and route on that service
function call_url(route) {
  return `http://127.0.0.1:9000/${route}`
}

function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function measureCall(numDiscardedCalls, numTimedCalls) {
  // Result:
  sumOfAllCalls = 0

  // Variables created once.
  var result

  // Perform requests discarding the first numDiscardedCalls.
  for (let i = 0; i < numDiscardedCalls + numTimedCalls; i++) {
    var start = Date.now();
    result = await fetch(call_url('bench-text'), {
      method: 'POST',
      body: 'Test',
      headers: { 'Content-Type': 'text/plain' }
    })
    await result.text()
    var callDuation = Date.now() - start;

    // Postprocessing.
    if (i >= numDiscardedCalls) {
      sumOfAllCalls += callDuation
      if (numTimedCalls < 50) {
        console.log(`Durations: ${i - numDiscardedCalls}: ${callDuation} ms`)
      }
    }
    await sleep(100)
  }
  return sumOfAllCalls
}

async function measureOneWayCall(numDiscardedCalls, numTimedCalls) {
  // Results:
  sumOfAllRequests = 0
  sumOfAllResponses = 0

  // Create variables once.
  var result
  var remoteStamp, localStamp

  // Perform requests discarding the first numDiscardedCalls.
  for (let i = 0; i < numDiscardedCalls + numTimedCalls; i++) {
    var start = Date.now();
    result = await fetch(call_url('bench-text-one-way'), {
      method: 'POST',
      body: 'Test',
      headers: { 'Content-Type': 'text/plain' }
    })
    remoteStamp = await result.text()
    localStamp = Date.now();

    // Postprocessing.
    var oneWayCall = parseInt(remoteStamp) - start;
    var responseCall = localStamp - parseInt(remoteStamp)
    if (i >= numDiscardedCalls) {
      sumOfAllRequests += oneWayCall
      sumOfAllResponses += responseCall
      if (numTimedCalls < 50) {
        console.log(`Durations: ${i - numDiscardedCalls}: ${oneWayCall} ms`)
      }
    }
    await sleep(100)
  }
  return [sumOfAllRequests, sumOfAllResponses]
}

// main method
async function main() {
  numTimedCalls = 100
  sumOfAllCalls = await measureCall(10, numTimedCalls)
  averageCallDuration = sumOfAllCalls / numTimedCalls
  console.log(`Average service call duration: ${averageCallDuration} ms`)

  {
    let [sumOfAllRequests, sumOfAllResponses] = await measureOneWayCall(10, numTimedCalls)
    averageRequestDuration = sumOfAllRequests / numTimedCalls
    averageResponseDuration = sumOfAllResponses / numTimedCalls
    console.log(`Average service request duration: ${averageRequestDuration} ms`)
    console.log(`Average service response duration: ${averageResponseDuration} ms`)
  }
}

// invoke main
main()