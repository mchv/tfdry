module "child" {
  source = "./modules/child"

  bool_as_string   = true
  number_as_string = 42
  string_as_bool   = "true"
  string_as_number = "42.5"
}
