#!/bin/bash

API_URL="http://localhost:8080/api/v1"
SUFFIX=$(date +%s)

echo "1️⃣ Creating 'fact_generator_$SUFFIX' tool..."
FACT_RESPONSE=$(curl -s -X POST "$API_URL/tools" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "fact_generator_'"$SUFFIX"'",
    "description": "Generates a random short fact about a given topic.",
    "input_schema": {"type":"object","properties":{"topic":{"type":"string"}},"required":["topic"]},
    "output_schema": {"type":"object","properties":{"output":{"type":"string"}},"required":["output"]},
    "handler_type": "llm",
    "handler_config": {"system_prompt": "Generate one very short, one-sentence fact about the requested topic.", "temperature": 0.7}
  }')
FACT_TOOL_ID=$(echo $FACT_RESPONSE | jq -r '.data.id // empty')

echo "2️⃣ Creating 'synthesizer_$SUFFIX' tool..."
SYNTH_RESPONSE=$(curl -s -X POST "$API_URL/tools" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "synthesizer_'"$SUFFIX"'",
    "description": "Takes two distinct facts or sentences and weaves them smoothly into a single, cohesive sentence.",
    "input_schema": {"type":"object","properties":{"fact1":{"type":"string"}, "fact2":{"type":"string"}},"required":["fact1", "fact2"]},
    "output_schema": {"type":"object","properties":{"output":{"type":"string"}},"required":["output"]},
    "handler_type": "llm",
    "handler_config": {"system_prompt": "Combine the two provided facts into a single, beautifully flowing sentence.", "temperature": 0.5}
  }')
SYNTH_TOOL_ID=$(echo $SYNTH_RESPONSE | jq -r '.data.id // empty')

echo "3️⃣ Creating 'translator_$SUFFIX' tool..."
TRANS_RESPONSE=$(curl -s -X POST "$API_URL/tools" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "translator_'"$SUFFIX"'",
    "description": "Translates text into French",
    "input_schema": {"type":"object","properties":{"text":{"type":"string"}},"required":["text"]},
    "output_schema": {"type":"object","properties":{"output":{"type":"string"}},"required":["output"]},
    "handler_type": "llm",
    "handler_config": {"system_prompt": "Translate the given text into French.", "temperature": 0.2}
  }')
TRANS_TOOL_ID=$(echo $TRANS_RESPONSE | jq -r '.data.id // empty')


echo "4️⃣ Creating Complex Agent..."
AGENT_RESPONSE=$(curl -s -X POST "$API_URL/agents" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Intelligent Synthesizer '"$SUFFIX"'",
    "tool_ids": ["'"$FACT_TOOL_ID"'", "'"$SYNTH_TOOL_ID"'", "'"$TRANS_TOOL_ID"'"],
    "model_config": {"max_token_budget": 10000}
  }')
AGENT_ID=$(echo $AGENT_RESPONSE | jq -r '.data.id // empty')

echo "5️⃣ Triggering DAG execution..."
RUN_RESPONSE=$(curl -s -X POST "$API_URL/agents/$AGENT_ID/run" \
  -H "Content-Type: application/json" \
  -d '{
    "goal": "Generate a short fact about space and a short fact about the ocean. Synthesize them into a single coherent sentence. Finally, translate that synthesized sentence into French."
  }')
RUN_ID=$(echo $RUN_RESPONSE | jq -r '.data.run_id // empty')

if [ -z "$RUN_ID" ]; then
  echo "❌ Failed to start run. Exiting."
  exit 1
fi
echo "   ✅ Run started: $RUN_ID"

echo "6️⃣ Polling execution status..."
for i in {1..20}; do
  STATUS_RESPONSE=$(curl -s "$API_URL/runs/$RUN_ID/status")
  STATUS=$(echo $STATUS_RESPONSE | jq -r '.data.status // empty')
  COMPLETION=$(echo $STATUS_RESPONSE | jq -r '.data.completion_pct // 0')
  
  echo "   ⏳ Status: $STATUS ($COMPLETION%)"
  
  if [ "$STATUS" == "success" ] || [ "$STATUS" == "failed" ]; then
    echo "   🎉 Final Outputs:"
    echo $STATUS_RESPONSE | jq '.data.node_outputs'
    break
  fi
  sleep 2
done

echo "7️⃣ Fetching Event Trace to verify parallel execution..."
curl -s "$API_URL/runs/$RUN_ID/trace" | jq '.data[] | {event_type, node_id, duration_ms}'
