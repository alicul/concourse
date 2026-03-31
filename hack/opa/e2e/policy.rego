package concourse

import rego.v1

deny contains msg if {
  input.action == "SetPipeline"
  not input.origin_pipeline
  msg := "SetPipeline action is missing origin_pipeline field"
}

deny contains msg if {
  input.action == "SetPipeline"
  not input.pipeline
  msg := "SetPipeline action is missing pipeline field"
}

set_pipeline_inputs contains result if {
  input.action == "SetPipeline"
  result := {
    "pipeline": input.pipeline,
    "origin_pipeline": input.origin_pipeline,
    "team": input.team,
  }
}
