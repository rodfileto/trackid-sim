// Score answers the fingerprint half of the identity question: do impressions
// of one generated master print match each other, and not other fingers,
// under a real matcher?
//
// SourceAFIS for Java (Apache-2.0) is the reference here. It stays the oracle
// a Go port in trackid-vision is later checked against, pair by pair.
//
// Reads the manifest written by tools/fpgen/gen_impressions.py (or by the Go
// port, `trackid-sim fingerprints`, in the same layout) and writes one
// CSV row per scored pair:
//
//	genuine_rr   rolled vs rolled, same finger (every pair)
//	impostor_rr  rolled 0 vs rolled 1 of every other finger
//	genuine_lr   latent vs every rolled impression of its finger
//	impostor_lr  latent vs rolled 0 of every other finger
//
// Usage (Java 11+ runs a single source file directly):
//
//	./fetch-deps.sh
//	java -cp 'lib/*' Score.java ../fpgen/out/impressions scores.csv

import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import com.machinezoo.sourceafis.FingerprintImage;
import com.machinezoo.sourceafis.FingerprintImageOptions;
import com.machinezoo.sourceafis.FingerprintMatcher;
import com.machinezoo.sourceafis.FingerprintTemplate;

import java.io.PrintWriter;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.stream.IntStream;

public class Score {
    record Pair(String kind, String probe, String candidate) {}

    public static void main(String[] args) throws Exception {
        Path dir = Path.of(args.length > 0 ? args[0] : "../fpgen/out/impressions");
        Path out = Path.of(args.length > 1 ? args[1] : "scores.csv");
        JsonObject manifest = JsonParser.parseString(Files.readString(dir.resolve("manifest.json"))).getAsJsonObject();
        int dpi = manifest.get("dpi").getAsInt();

        List<List<String>> rolled = new ArrayList<>();
        List<List<String>> latent = new ArrayList<>();
        for (var f : manifest.getAsJsonArray("fingers")) {
            var o = f.getAsJsonObject();
            List<String> r = new ArrayList<>();
            o.getAsJsonArray("rolled").forEach(e -> r.add(e.getAsString()));
            List<String> l = new ArrayList<>();
            o.getAsJsonArray("latent").forEach(e -> l.add(e.getAsJsonObject().get("file").getAsString()));
            rolled.add(r);
            latent.add(l);
        }

        List<String> files = new ArrayList<>();
        rolled.forEach(files::addAll);
        latent.forEach(files::addAll);
        long t0 = System.nanoTime();
        Map<String, FingerprintTemplate> templates = new ConcurrentHashMap<>();
        var options = new FingerprintImageOptions().dpi(dpi);
        files.parallelStream().forEach(name -> {
            try {
                var image = new FingerprintImage(Files.readAllBytes(dir.resolve(name)), options);
                templates.put(name, new FingerprintTemplate(image));
            } catch (Exception e) {
                throw new RuntimeException(name, e);
            }
        });
        System.err.printf("%d templates in %.1f s%n", templates.size(), (System.nanoTime() - t0) / 1e9);

        int n = rolled.size();
        List<Pair> pairs = new ArrayList<>();
        for (int a = 0; a < n; a++) {
            var r = rolled.get(a);
            for (int i = 0; i < r.size(); i++)
                for (int j = i + 1; j < r.size(); j++)
                    pairs.add(new Pair("genuine_rr", r.get(i), r.get(j)));
            for (int b = 0; b < n; b++)
                if (b != a)
                    pairs.add(new Pair("impostor_rr", r.get(0), rolled.get(b).get(1)));
            for (String l : latent.get(a)) {
                for (String c : r)
                    pairs.add(new Pair("genuine_lr", l, c));
                for (int b = 0; b < n; b++)
                    if (b != a)
                        pairs.add(new Pair("impostor_lr", l, rolled.get(b).get(0)));
            }
        }

        t0 = System.nanoTime();
        double[] scores = new double[pairs.size()];
        IntStream.range(0, pairs.size()).parallel().forEach(k -> {
            var p = pairs.get(k);
            scores[k] = new FingerprintMatcher(templates.get(p.probe())).match(templates.get(p.candidate()));
        });
        System.err.printf("%d pairs in %.1f s%n", pairs.size(), (System.nanoTime() - t0) / 1e9);

        try (var w = new PrintWriter(Files.newBufferedWriter(out))) {
            w.println("kind,probe,candidate,score");
            for (int k = 0; k < pairs.size(); k++) {
                var p = pairs.get(k);
                w.printf("%s,%s,%s,%.4f%n", p.kind(), p.probe(), p.candidate(), scores[k]);
            }
        }
        System.err.println("wrote " + out);
    }
}
