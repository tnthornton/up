oxr = option("params").oxr
items = [{
    apiVersion: "nop.crossplane.io/v1alpha1"
    kind: "NopResource"
    metadata.name = oxr.metadata.name
    spec.forProvider = {
      conditionAfter = [{
        conditionStatus: "True"
        conditionType: "Ready"
        time: "5s"
      }]
      fields = {
        ipCidrRange: oxr.spec.parameters.ipCidrRange
        networkRef: oxr.spec.parameters.networkRef
        region: oxr.spec.parameters.region
        secondaryIpRange: oxr.spec.parameters.secondaryIpRange
      }
    }
}]

