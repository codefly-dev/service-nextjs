import useSWR from "swr";
import { JSONView } from "./json-view";
import { useEffect, useState } from "react";

function MessageWithCode({ option }) {
  const message = "In the code, we fetch the data with the SDK";
  let code = `codefly({ endpoint: "iam/people", get: "/version" })`;
  if (option === "gateway") {
    code = `codefly({ endpoint: "api/gateway", get: "/iam/people/version" }) `;
  }

  return (
    <div className="mb-[20px]">
      <p>{message}</p>
      <pre className="bg-[#5A5A5A] p-[10px] rounded-[5px]">
        <code>{code}</code>
      </pre>
    </div>
  );
}

export function FetchAndDisplayJSON({
  endpoints,
}: {
  endpoints: Record<string, string>;
}) {
  const [selectedOption, setSelectedOption] = useState("service");
  const [isProtected, setIsProtected] = useState(false);
  const [url, setUrl] = useState("");

  useEffect(() => {
    update(selectedOption);
  }, []);

  function codefly({ endpoint, get }: { endpoint: string; get: string }) {
    endpoint = endpoint.replace("/", "__");
    const envString = `CODEFLY-ENDPOINT__${endpoint}____REST`.toUpperCase();
    return endpoints[envString] + get;
  }

  const handleDropdownChange = (event) => {
    const value = event.target.value;
    setSelectedOption(value);
    update(value);
  };

  const update = (value) => {
    if (value === "service") {
      setUrl(codefly({ endpoint: "iam/people", get: "/version" }));
    } else if (value === "gateway") {
      setUrl(codefly({ endpoint: "api/gateway", get: "/iam/people/version" }));
    }
  };

  const handleCheckboxChange = (event) => {
    setIsProtected(event.target.checked);
  };

  const { data: accessToken } = useSWR("/api/access-token", (url) =>
    fetch(url).then((res) => res.json())
  );

  const { data, error, isLoading } = useSWR(
    !isProtected
      ? [url]
      : isProtected && accessToken
      ? [url, accessToken]
      : null,
    ([url, accessToken]) =>
      fetch(url, {
        headers: { Authorization: `Bearer ${accessToken?.data}` },
      }).then((res) => res.json())
  );

  return (
    <div>
      <div className="grid gap-1" style={{ marginBottom: "20px" }}>
        <select
          value={selectedOption}
          onChange={handleDropdownChange}
          style={{
            padding: "10px",
            borderRadius: "5px",
            border: "1px solid #ddd",
          }}
        >
          <option value="">Where do we get the data?</option>
          <option value="service">Service</option>
          <option value="gateway">Gateway</option>
        </select>
        <div style={{ marginTop: "10px" }}>
          <input
            type="checkbox"
            checked={isProtected}
            onChange={handleCheckboxChange}
          />
          <label style={{ marginLeft: "5px" }}>Protected</label>
        </div>
      </div>
      <MessageWithCode option={selectedOption} />
      <div>
        {isLoading ? (
          <pre>Loading...</pre>
        ) : error || !data ? (
          <pre>Error loading data</pre>
        ) : (
          <JSONView>{JSON.stringify(data, null, 2)}</JSONView>
        )}
      </div>
    </div>
  );
}
