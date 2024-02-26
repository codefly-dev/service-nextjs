const API_URL = process.env["CODEFLY-ENDPOINT__APP__SVC__COOL__REST"];

export default async function PublicRoute(req, res) {
  try {
    const response = await fetch(API_URL);
    const data = await response.json();
    res.status(200).json(data);
  } catch (e) {
    res.status(500).json({ error: "Unable to fetch", description: e });
  }
}
