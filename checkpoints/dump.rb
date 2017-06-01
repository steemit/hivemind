#!/usr/local/bin/ruby

WSURL = "ws://0.0.0.0:8090/ws"

require 'progress_bar'
require 'faye/websocket'
require 'eventmachine'

def stream_blocks_from_ws n1, n2
  EM.run do
    pending = 0
    loaded  = 0
    expect  = n1
    buffer  = (n1..n2).to_a

    pb = ProgressBar.new(buffer.size)
    ws = Faye::WebSocket::Client.new(WSURL)

    ws.on :open do |event|
      100.times do
        n = buffer.shift
        pending += 1
        ws.send("{\"id\":#{n},\"method\":\"call\",\"params\":[0,\"get_block\",[#{n},false]]}")
      end
    end

    ws.on :message do |event|
      num, block = extract_block(event.data)
      raise "out of sequence: expected #{expect}, got #{num}" unless num == expect
      expect = num + 1
      yield block

      loaded  += 1
      pending -= 1
      pb.increment!(1000) if loaded % 1000 == 0

      n = buffer.shift
      if n
        pending += 1
        ws.send("{\"id\":#{n},\"method\":\"call\",\"params\":[0,\"get_block\",[#{n},false]]}")
      elsif pending == 0
        ws.close
      end
    end

    ws.on :close do |event|
      ws = nil
      EM.stop_event_loop
    end
  end
end

def extract_block(data)
  m = data.match(/^\{"id":(\d+),"result":(.+)\}$/)
  raise "unexpected body: #{data[0,64]}" unless m
  [m[1].to_i, m[2]]
end

def stream_blocks_to_file n1, n2, file
  raise "File already exists" if File.exists?(file)
  File.open(file, 'w') do |f|
    stream_blocks_from_ws(n1, n2){|r| f.write(r[1]+"\n")}
  end
end

# Save all blocks up to 12M in batches of 1M
(1..12).each do |mil|
  n1 = (mil - 1) * 1000000 + 1
  n2 = mil * 1000000
  stream_blocks_to_file n1, n2, "#{n2}.json.lst"
end
