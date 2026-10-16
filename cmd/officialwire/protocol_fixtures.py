"""Synthetic, credential-free SSE fixtures for native OpenCode protocol probes."""
import json

MODEL = 'space-bunny-free'
CALL_ID = 'call_fake_read'


def protocol_spec(protocol):
    """Return SDK package, relative base URL, and exact inference POST path."""
    specs = {
        'chat': ('@ai-sdk/openai-compatible', '/inference/openai/v1', '/inference/openai/v1/chat/completions'),
        'responses': ('@ai-sdk/openai', '/inference/openai/v1', '/inference/openai/v1/responses'),
        'anthropic': ('@ai-sdk/anthropic', '/inference/anthropic/v1', '/inference/anthropic/v1/messages'),
        'gemini': ('@ai-sdk/google', '/inference/google/v1beta', '/inference/google/v1beta/models/space-bunny-free:streamGenerateContent?alt=sse'),
    }
    try:
        return specs[protocol]
    except KeyError:
        raise ValueError('Unsupported protocol: ' + str(protocol)) from None


def response_frames(protocol, tool_args=None):
    """Return a complete SSE byte stream, optionally asking for the read tool."""
    protocol_spec(protocol)
    if tool_args is not None and not isinstance(tool_args, dict):
        raise TypeError('tool_args must be a dict or None')
    is_tool = tool_args is not None
    arguments = json.dumps(tool_args, separators=(',', ':')) if is_tool else None
    frames = []

    def emit(data, event=None):
        frames.append(('event: ' + event + '\n' if event else '') +
                      'data: ' + json.dumps(data, separators=(',', ':')) + '\n\n')

    if protocol == 'chat':
        delta = {'role': 'assistant'}
        if is_tool:
            delta['tool_calls'] = [{'index': 0, 'id': CALL_ID, 'type': 'function',
                                    'function': {'name': 'read', 'arguments': arguments}}]
        else:
            delta['content'] = 'OK'
        base = {'id': 'chatcmpl-local-wire', 'object': 'chat.completion.chunk',
                'created': 1700000000, 'model': MODEL}
        emit(dict(base, choices=[{'index': 0, 'delta': delta, 'finish_reason': None}]))
        emit(dict(base, choices=[{'index': 0, 'delta': {}, 'finish_reason': 'tool_calls' if is_tool else 'stop'}],
                  usage={'prompt_tokens': 10, 'completion_tokens': 1, 'total_tokens': 11}))
        frames.append('data: [DONE]\n\n')
    elif protocol == 'responses':
        seq = 0

        def event(kind, **fields):
            nonlocal seq
            emit(dict(type=kind, sequence_number=seq, **fields), kind)
            seq += 1

        response = {'id': 'resp_local_wire', 'object': 'response', 'created_at': 1700000000,
                    'model': MODEL, 'status': 'in_progress', 'error': None,
                    'incomplete_details': None, 'output': []}
        event('response.created', response=response)
        if is_tool:
            item = {'id': 'fc_local_read', 'type': 'function_call', 'call_id': CALL_ID,
                    'name': 'read', 'arguments': '', 'status': 'in_progress'}
            event('response.output_item.added', output_index=0, item=item)
            event('response.function_call_arguments.delta', item_id=item['id'], output_index=0, delta=arguments)
            event('response.function_call_arguments.done', item_id=item['id'], output_index=0, arguments=arguments)
            completed_item = dict(item, arguments=arguments, status='completed')
        else:
            item = {'id': 'msg_local_wire', 'type': 'message', 'role': 'assistant',
                    'status': 'in_progress', 'content': []}
            part = {'type': 'output_text', 'text': '', 'annotations': [], 'logprobs': []}
            event('response.output_item.added', output_index=0, item=item)
            event('response.content_part.added', item_id=item['id'], output_index=0, content_index=0, part=part)
            event('response.output_text.delta', item_id=item['id'], output_index=0, content_index=0, delta='OK', logprobs=[])
            event('response.output_text.done', item_id=item['id'], output_index=0, content_index=0, text='OK', logprobs=[])
            completed_part = dict(part, text='OK')
            event('response.content_part.done', item_id=item['id'], output_index=0, content_index=0, part=completed_part)
            completed_item = dict(item, status='completed', content=[completed_part])
        event('response.output_item.done', output_index=0, item=completed_item)
        event('response.completed', response=dict(response, status='completed', output=[completed_item],
              usage={'input_tokens': 10, 'output_tokens': 1, 'total_tokens': 11,
                     'input_tokens_details': {'cached_tokens': 0}, 'output_tokens_details': {'reasoning_tokens': 0}}))
    elif protocol == 'anthropic':
        def event(kind, **fields):
            emit(dict(type=kind, **fields), kind)

        event('message_start', message={'id': 'msg_local_wire', 'type': 'message', 'role': 'assistant',
              'model': MODEL, 'content': [], 'stop_reason': None, 'stop_sequence': None,
              'usage': {'input_tokens': 10, 'output_tokens': 0}})
        block = ({'type': 'tool_use', 'id': CALL_ID, 'name': 'read', 'input': {}} if is_tool
                 else {'type': 'text', 'text': ''})
        event('content_block_start', index=0, content_block=block)
        delta = ({'type': 'input_json_delta', 'partial_json': arguments} if is_tool
                 else {'type': 'text_delta', 'text': 'OK'})
        event('content_block_delta', index=0, delta=delta)
        event('content_block_stop', index=0)
        event('message_delta', delta={'stop_reason': 'tool_use' if is_tool else 'end_turn', 'stop_sequence': None},
              usage={'output_tokens': 1})
        event('message_stop')
    else:
        part = ({'functionCall': {'id': CALL_ID, 'name': 'read', 'args': tool_args}} if is_tool
                else {'text': 'OK'})
        emit({'candidates': [{'index': 0, 'content': {'role': 'model', 'parts': [part]}, 'finishReason': 'STOP'}],
              'usageMetadata': {'promptTokenCount': 10, 'candidatesTokenCount': 1, 'totalTokenCount': 11},
              'modelVersion': MODEL, 'responseId': 'gemini_local_wire'})
    return ''.join(frames).encode('utf-8')


def tool_result(protocol, body):
    """Extract read's result from a continuation request, or reject missing output."""
    protocol_spec(protocol)

    def text(value):
        return value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)

    if protocol == 'chat':
        for message in body.get('messages', []):
            if message.get('role') == 'tool' and message.get('tool_call_id') == CALL_ID:
                return text(message.get('content', ''))
    elif protocol == 'responses':
        for item in body.get('input', []):
            if isinstance(item, dict) and item.get('type') == 'function_call_output' and item.get('call_id') == CALL_ID:
                return text(item.get('output', ''))
    elif protocol == 'anthropic':
        for message in body.get('messages', []):
            content = message.get('content', [])
            if message.get('role') != 'user' or not isinstance(content, list):
                continue
            for block in content:
                if block.get('type') == 'tool_result' and block.get('tool_use_id') == CALL_ID:
                    return text(block.get('content', ''))
    else:
        for content in body.get('contents', []):
            for part in content.get('parts', []):
                result = part.get('functionResponse', {})
                # Gemini clients may omit the optional ID and correlate by name.
                if result.get('name') == 'read' and result.get('id', CALL_ID) == CALL_ID:
                    return text(result.get('response', ''))
    raise ValueError('Matching read tool result missing in ' + protocol + ' request')
