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
        autoCreateSubnetworks: oxr.spec.parameters.autoCreateSubnetworks
        routingMode: oxr.spec.parameters.routingMode
      }
    }
}]

