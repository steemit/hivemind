#!/usr/local/bin/python3
import json
import websocket

def vals_sorted_by_key(adict):
    ret = []
    for key in sorted(adict.keys()):
        ret.append(adict[key])
    return ret


def call_batch_ws(url, calls):
    class local:
        pending = 0
        queue = []
        results = {}

    def on_message(ws, message):
        msg = json.loads(message)
        local.results[msg['id']] = msg['result']
        local.pending -= 1
        if not local.pending and not local.queue:
            ws.close()

    def on_error(ws, error):
        print("[WS] ERROR: {}".format(error))

    def on_close(ws):
        pass

    def on_open(ws):
        id = 0
        while local.queue:
            local.pending += 1
            method, params = local.queue.pop()
            message = dict(jsonrpc='2.0', method=method, id=id, params=params)
            ws.send(json.dumps(message))
            id += 1

    # init
    calls.reverse()
    local.queue = calls
    ws = websocket.WebSocketApp(url,
        on_message = on_message,
        on_error = on_error,
        on_close = on_close)

    # send
    ws.on_open = on_open
    ws.run_forever()

    return vals_sorted_by_key(local.results)


if __name__ == "__main__":
    url = "ws://localhost:8090"
    calls = [
        ["get_block", [1000]],
        ["get_block", [1001]],
        ["get_block", [1002]],
    ]
    ret = call_batch_ws(url, calls)
    print(ret)
