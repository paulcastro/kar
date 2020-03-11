# Deploying KAR on Kubernetes

## Prerequisites

1. You need a Kubernetes 1.16 (or newer) cluster.

2. You will need the `kubectl` cli installed locally.

3. You will need the `helm` (Helm 3) cli installed locally.

4. You will need a local git clone of this repository.

## Prepare the `kar-system` namespace

Before deploying KAR for the first time on a cluster, you will need to
create and configure the `kar-system` namespace.  This namespace will
be used to execute KAR system components.

Perform the following operations:
1. Create the kar-system namespace
```shell
kubectl create ns kar-system
```

2. Create an image pull secret that allows access to the KAR
namespaces in the IBM container registry (kar-dev, kar-stage,
kar-prod). Currently, you will have to ask Dave or Olivier for an
apikey that enables this access. After you have that apikey execute
the command below (replacing KAR_CR_APIKEY with the actual value).

```shell
kubectl --namespace kar-system create secret docker-registry kar.ibm.com.image-pull --docker-server=us.icr.io --docker-username=iamapikey --docker-email=kar@ibm.com --docker-password=KAR_CR_APIKEY
```

## Install the KAR Helm chart
From the top-level of a git clone of the KAR repo.

```shell
helm install kar charts/kar -n kar-system
```

If you are doing development on KAR itself, you can use the usual Helm operations to deploy upgrades, override default values, etc.  See [../charts/README.md](../charts/README.md) for more details on enabling non-default configurations of K AR.