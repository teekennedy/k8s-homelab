variable "name" {
  type = string
}

variable "namespace" {
  type = string
}

variable "data" {
  type = map(string)
}

variable "annotations" {
  type    = map(string)
  default = {}
}
