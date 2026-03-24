INSERT INTO tools (id, name, description, input_schema, output_schema, handler_type, handler_config)
VALUES
  (gen_random_uuid(), 'echo',
   'Returns the input message unchanged',
   '{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}',
   '{"type":"object","properties":{"output":{"type":"string"}},"required":["output"]}',
   'internal', '{}'),

  (gen_random_uuid(), 'concat',
   'Concatenates two strings with a space between them',
   '{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"]}',
   '{"type":"object","properties":{"output":{"type":"string"}},"required":["output"]}',
   'internal', '{}');
