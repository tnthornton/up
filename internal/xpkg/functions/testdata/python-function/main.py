from crossplane.function import resource
from crossplane.function.proto.v1 import run_function_pb2 as fnv1


def compose(req: fnv1.RunFunctionRequest, rsp: fnv1.RunFunctionResponse):
    # Get the region from the observed XR.
    region = req.observed.composite.resource["spec"]["region"]

    # Compose an S3 bucket using the region.
    resource.update(req.desired.resources["bucket"], {
        "apiVersion": "s3.aws.upbound.io/v1beta2",
        "kind": "Bucket",
        "spec": {
            "forProvider": {
                "region": region,
            },
        }
    })

