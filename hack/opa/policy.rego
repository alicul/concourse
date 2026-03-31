package concourse

import rego.v1

default decision := {"allowed": true}

# uncomment to include deny rules
#decision := {"allowed": false, "reasons": reasons} if {
#  count(deny) > 0
#  reasons := deny
#}

deny contains "cannot use docker-image types" if {
  input.action == "UseImage"
  input.data.image_type == "docker-image"
}

deny contains "cannot run privileged tasks" if {
  input.action == "SaveConfig"
  input.data.jobs[_].plan[_].privileged
}

deny contains "cannot use privileged resource types" if {
  input.action == "SaveConfig"
  input.data.resource_types[_].privileged
}
